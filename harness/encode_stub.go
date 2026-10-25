//go:build noencoder

package main

import "errors"

// encoderBuilt is false in a build with -tags noencoder, for a fork commit
// whose encoder API this harness does not compile against.
const encoderBuilt = false

func cmdEncode([]string) error {
	return errors.New("encode: this binary was built with -tags noencoder")
}
