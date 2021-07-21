package tx

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/unknownproto"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
)

// DefaultTxDecoder returns a default protobuf TxDecoder using the provided Marshaler.
func DefaultTxDecoder(cdc codec.ProtoCodecMarshaler) sdk.TxDecoder {
	return func(txBytes []byte) (sdk.Tx, error) {
		// Make sure txBytes adheres to ADR-027.
		err := rejectNonADR027(txBytes)
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		var raw tx.TxRaw

		// reject all unknown proto fields in the root TxRaw
		err = unknownproto.RejectUnknownFieldsStrict(txBytes, &raw, cdc.InterfaceRegistry())
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(txBytes, &raw)
		if err != nil {
			return nil, err
		}

		var body tx.TxBody

		// allow non-critical unknown fields in TxBody
		txBodyHasUnknownNonCriticals, err := unknownproto.RejectUnknownFields(raw.BodyBytes, &body, true, cdc.InterfaceRegistry())
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(raw.BodyBytes, &body)
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		var authInfo tx.AuthInfo

		// reject all unknown proto fields in AuthInfo
		err = unknownproto.RejectUnknownFieldsStrict(raw.AuthInfoBytes, &authInfo, cdc.InterfaceRegistry())
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		err = cdc.Unmarshal(raw.AuthInfoBytes, &authInfo)
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		theTx := &tx.Tx{
			Body:       &body,
			AuthInfo:   &authInfo,
			Signatures: raw.Signatures,
		}

		return &wrapper{
			tx:                           theTx,
			bodyBz:                       raw.BodyBytes,
			authInfoBz:                   raw.AuthInfoBytes,
			txBodyHasUnknownNonCriticals: txBodyHasUnknownNonCriticals,
		}, nil
	}
}

// DefaultJSONTxDecoder returns a default protobuf JSON TxDecoder using the provided Marshaler.
func DefaultJSONTxDecoder(cdc codec.ProtoCodecMarshaler) sdk.TxDecoder {
	return func(txBytes []byte) (sdk.Tx, error) {
		var theTx tx.Tx
		err := cdc.UnmarshalJSON(txBytes, &theTx)
		if err != nil {
			return nil, sdkerrors.Wrap(sdkerrors.ErrTxDecode, err.Error())
		}

		return &wrapper{
			tx: &theTx,
		}, nil
	}
}

// rejectNonADR027 rejects txBytes that do not adhere to ADR-027. This function
// only checks that field numbers are in ascending order, as all other ADR-027
// edge cases (e.g. TxRaw fields having default values) will not happen with
// TxRaw.
func rejectNonADR027(txBytes []byte) error {
	// Make sure all fields are ordered in ascending order.
	prevTagNum := protowire.Number(0)
	for len(txBytes) > 0 {
		tagNum, _, m := protowire.ConsumeField(txBytes)
		if m < 0 {
			return errors.New("invalid length")
		}

		if tagNum < prevTagNum {
			return fmt.Errorf("txRaw must follow ADR-027, got tagNum %d after tagNum %d", tagNum, prevTagNum)
		}
		prevTagNum = tagNum

		// Skip over the bytes that store fieldNumber and wireType bytes.
		txBytes = txBytes[m:]
	}

	return nil
}
