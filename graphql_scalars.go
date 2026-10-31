package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/99designs/gqlgen/graphql"
)

// UnitNumber stays a string in Go so JSON decoding preserves all uint32 values
// exactly and existing RCON builders never pass through a floating-point value.
type UnitNumber = string

// FiniteFloat stays a float64 in Go; GraphQL coercion rejects NaN and infinity
// before a resolver can build an RCON command.
type FiniteFloat = float64

func MarshalUnitNumber(value string) graphql.Marshaler {
	return graphql.WriterFunc(func(w io.Writer) {
		_, _ = io.WriteString(w, strconv.Quote(value))
	})
}

func UnmarshalUnitNumber(value any) (string, error) {
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case json.Number:
		text = value.String()
	case int:
		text = strconv.Itoa(value)
	case int32:
		text = strconv.FormatInt(int64(value), 10)
	case int64:
		text = strconv.FormatInt(value, 10)
	case uint:
		text = strconv.FormatUint(uint64(value), 10)
	case uint32:
		text = strconv.FormatUint(uint64(value), 10)
	case uint64:
		text = strconv.FormatUint(value, 10)
	case float64:
		if math.Trunc(value) != value {
			return "", fmt.Errorf("UnitNumber must be an integer from 1 to 4294967295")
		}
		text = strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return "", fmt.Errorf("UnitNumber must be a decimal string or integer, got %T", value)
	}
	parsed, err := strconv.ParseUint(text, 10, 32)
	if err != nil || parsed == 0 {
		return "", fmt.Errorf("UnitNumber must be an integer from 1 to 4294967295")
	}
	return strconv.FormatUint(parsed, 10), nil
}

func MarshalFiniteFloat(value float64) graphql.Marshaler {
	return graphql.WriterFunc(func(w io.Writer) {
		_, _ = io.WriteString(w, strconv.FormatFloat(value, 'g', -1, 64))
	})
}

func UnmarshalFiniteFloat(value any) (float64, error) {
	var number float64
	var err error
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int32:
		number = float64(value)
	case int64:
		number = float64(value)
	case json.Number:
		number, err = value.Float64()
	default:
		return 0, fmt.Errorf("FiniteFloat must be numeric, got %T", value)
	}
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("FiniteFloat must be finite")
	}
	return number, nil
}
