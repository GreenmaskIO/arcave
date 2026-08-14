package model

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
)

// RawSettings is one provider's own configuration, carried verbatim. arcave
// reads none of it: the control plane hands it to the provider's factory, and
// what the keys mean is the provider's alone.
type RawSettings map[string]any

// Decode fills target from the raw settings, refusing a key no field claims —
// the strictness the outer config cannot apply to keys it does not know.
//
// The hooks are the ones viper applies to a typed decode: values under an
// untyped section arrive as they were written, so a duration is a string until
// something converts it.
func (r RawSettings) Decode(target any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      target,
		ErrorUnused: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return fmt.Errorf("build settings decoder: %w", err)
	}
	if err := decoder.Decode(map[string]any(r)); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	return nil
}
