package cosmovisor

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/otiai10/copy"
)

type Launcher struct {
	cfg *Config
	fw  *fileWatcher
}

func NewLauncher(cfg *Config) (Launcher, error) {
	fw, err := newUpgradeFileWatcher(cfg.UpgradeInfoFilePath(), cfg.PoolInterval)
	return Launcher{cfg, fw}, err
}

// Run a subprocess and returns when the subprocess exits,
// either when it dies, or *after* a successful upgrade.
func (l Launcher) Run(args []string, stdout, stderr io.Writer) (bool, error) {
	bin, err := l.cfg.CurrentBin()
	if err != nil {
		return false, fmt.Errorf("error creating symlink to genesis: %w", err)
	}

	if err := EnsureBinary(bin); err != nil {
		return false, fmt.Errorf("current binary is invalid: %w", err)
	}
	fmt.Println("[cosmovisor] running ", bin, args)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("launching process %s %s failed: %w", bin, strings.Join(args, " "), err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGQUIT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		if err := cmd.Process.Signal(sig); err != nil {
			log.Fatal(bin, "terminated. Error:", err)
		}
	}()

	needsUpdate, err := l.WaitForUpgradeOrExit(cmd)
	if err != nil || !needsUpdate {
		return false, err
	}

	if err := doBackup(l.cfg); err != nil {
		return false, err
	}

	return true, DoUpgrade(l.cfg, l.fw.currentInfo)
}

func doBackup(cfg *Config) error {
	// take backup if `UNSAFE_SKIP_BACKUP` is not set.
	if !cfg.UnsafeSkipBackup {
		// a destination directory, Format MM-DD-YYYY
		dt := time.Now()
		dst := fmt.Sprintf(cfg.Home+"/data"+"-backup-%s", dt.Format("01-22-2000"))

		// copy the $DAEMON_HOME/data to a backup dir
		err := copy.Copy(cfg.Home+"/data", dst)

		if err != nil {
			return fmt.Errorf("error while taking data backup: %w", err)
		}

		fmt.Println("Backup saved at ", dst)
	}

	return nil
}

// WaitResult is used to wrap feedback on cmd state with some mutex logic.
// This is needed as multiple go-routines can affect this - two read pipes that can trigger upgrade
// As well as the command, which can fail
type WaitResult struct {
	// both err and info may be updated from several go-routines
	// access is wrapped by mutex and should only be done through methods
	err   error
	info  *UpgradeInfo
	mutex sync.Mutex
}

// AsResult reads the data protected by mutex to avoid race conditions
func (u *WaitResult) AsResult() (*UpgradeInfo, error) {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	return u.info, u.err
}

// SetError will set with the first error using a mutex
// don't set it once info is set, that means we chose to kill the process
func (u *WaitResult) SetError(myErr error) {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	if u.info == nil && myErr != nil {
		u.err = myErr
	}
}

// WaitForUpgradeOrExit checks upgrade plan file created by the app.
// When it returns, the process (app) is finished.
//
// It returns (true, nil) if an upgrade should be initiated (and we killed the process)
// It returns (false, err) if the process died by itself, or there was an issue reading the upgrade-info file.
// It returns (false, nil) if the process exited normally without triggering an upgrade. This is very unlikely
// to happened with "start" but may happened with short-lived commands like `gaiad export ...`
func (l Launcher) WaitForUpgradeOrExit(cmd *exec.Cmd) (bool, error) {
	currentUpgradeName := l.cfg.UpgradeName()
	var cmdDone = make(chan error)
	go func() {
		cmdDone <- cmd.Wait()
	}()

	select {
	case <-l.fw.MonitorUpdate(currentUpgradeName):
		// upgrade - kill the process and restart
		_ = cmd.Process.Kill()
	case err := <-cmdDone:
		l.fw.Stop()
		// no error -> command exits normally (eg. short command like `gaiad version`)
		if err == nil {
			return false, nil
		}
		// the app x/upgrade causes a panic and the app can die before the filwatcher finds the
		// update, so we need to recheck update-info file.
		if !l.fw.CheckUpdate(currentUpgradeName) {
			return false, err
		}
	}
	return true, nil
}
