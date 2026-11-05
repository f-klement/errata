package core

import "fmt"

// ParseManifest is the tree of a manifest, data, the bytes of the file s
// names, read with parse after the node limit of format. name is the file
// kind the reader reads it as, for the message when errata was built
// without parse. skipBOM drops a byte order mark at its start, as npm and
// Cargo skip one; the mark holds no line end, so no line moves. A refusal of
// the node limit names the file and is ErrTooDense; parse's own error is
// returned as parse gave it, so the reader names the file. package_version
// and dependency read package.json, pyproject.toml and Cargo.toml this way.
func (s Source) ParseManifest(data []byte, name string, format *Format, parse func([]byte) (*Node, error), skipBOM bool) (*Node, error) {
	if parse == nil {
		return nil, fmt.Errorf("errata was built without the reader for %s", name)
	}
	if skipBOM {
		data = DropBOM(data)
	}
	if err := s.CheckNodes(data, format); err != nil {
		return nil, err
	}
	return parse(data)
}

// Member is the node at keys below n, nil when n is nil, a key is absent or
// a node on the way is not a mapping, whose Field is nil.
func (n *Node) Member(keys ...string) *Node {
	for _, k := range keys {
		if n == nil {
			return nil
		}
		n = n.Field[k]
	}
	return n
}
