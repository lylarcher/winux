//go:build !windows

package core

// Stubs so the package keeps cross-compiling; socket tables are read through
// the Windows IP Helper API.

func tcpRows() ([]sockRow, error) { return nil, &unsupportedError{"TCP table"} }

func udpRows() ([]sockRow, error) { return nil, &unsupportedError{"UDP table"} }

func routeRows() ([]routeRow, error) { return nil, &unsupportedError{"route table"} }

func ifRows() ([]ifRow, error) { return nil, &unsupportedError{"interface table"} }

func ifNameByIndex() map[int]string { return nil }

func gatherProtoStats() protoStats { return protoStats{} }
