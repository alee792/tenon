package reverse

import "context"

// Description is the one-line summary the managed MCP boundary publishes.
const Description = "Reverse a UTF-8 string by Unicode code point."

// Input is the tool's argument object.
type Input struct {
	Text string `json:"text"`
}

// Output is the tool's result object.
type Output struct {
	Reversed string `json:"reversed"`
}

// Execute reverses Text by runes so multi-byte characters stay intact.
func Execute(ctx context.Context, in Input) (Output, error) {
	runes := []rune(in.Text)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return Output{Reversed: string(runes)}, nil
}
