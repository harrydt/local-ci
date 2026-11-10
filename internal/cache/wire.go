package cache

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// int64JSON accepts a number or a string: protobuf JSON, which the cache
// client speaks, encodes int64 as a string.
type int64JSON int64

func (n *int64JSON) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		s = string(b)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	*n = int64JSON(v)
	return err
}

// blockIDs reads an Azure Put Block List body,
//
//	<BlockList><Latest>id</Latest><Uncommitted>id</Uncommitted>...</BlockList>
//
// and returns the ids in order. Every block is staged by this server, so the
// Latest/Committed/Uncommitted distinction doesn't matter.
func blockIDs(r io.Reader) ([]string, error) {
	dec := xml.NewDecoder(r)
	var ids []string
	depth := 0
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if depth == 2 { // a child of BlockList
				ids = append(ids, strings.TrimSpace(text.String()))
			}
			depth--
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("empty block list")
	}
	return ids, nil
}
