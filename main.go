// Command winux is the thin executable shell around the winux/core engine:
// all commands, the busybox-style dispatch and the console setup live in
// winux/core. Keeping this file a bare main keeps the engine importable by
// the test suite in winux/test.
package main

import "winux/core"

func main() {
	core.Main()
}
