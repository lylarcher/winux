//go:build !windows

package core

// Stubs so the package keeps cross-compiling; adapter and neighbour data come
// from the Windows IP Helper API.

func adapters() ([]adapterInfo, error) {
	return nil, &unsupportedError{"adapter list"}
}

func netNeighbors() ([]netNeighbor, error) {
	return nil, &unsupportedError{"neighbour table"}
}
