package search_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"camlistore.org/pkg/blob"
	"camlistore.org/pkg/index"
	"camlistore.org/pkg/index/indextest"
	. "camlistore.org/pkg/search"
	"camlistore.org/pkg/test"
)

// indexType is one of the three ways we test the query handler code.
type indexType int

var queryType = flag.String("querytype", "", "Empty for all query types, else 'classic', 'scan', or 'build'")

const (
	indexClassic     indexType = iota // sorted key/value pairs from index.Storage
	indexCorpusScan                   // *Corpus scanned from key/value pairs on start
	indexCorpusBuild                  // empty *Corpus, built iteratively as blob received.
)

var (
	allIndexTypes = []indexType{indexClassic, indexCorpusScan, indexCorpusBuild}
	memIndexTypes = []indexType{indexCorpusScan, indexCorpusBuild}
)

func (i indexType) String() string {
	switch i {
	case indexClassic:
		return "classic"
	case indexCorpusScan:
		return "scan"
	case indexCorpusBuild:
		return "build"
	default:
		return fmt.Sprintf("unknown-index-type-%d", i)
	}
}

type queryTest struct {
	t  *testing.T
	id *indextest.IndexDeps

	Handler func() *Handler
}

func querySetup(t *testing.T) (*indextest.IndexDeps, *Handler) {
	idx := index.NewMemoryIndex() // string key-value pairs in memory, as if they were on disk
	id := indextest.NewIndexDeps(idx)
	id.Fataler = t
	h := NewHandler(idx, id.SignerBlobRef)
	return id, h
}

func testQuery(t *testing.T, fn func(*queryTest)) {
	testQueryTypes(t, allIndexTypes, fn)
}

func testQueryTypes(t *testing.T, types []indexType, fn func(*queryTest)) {
	defer test.TLog(t)()
	for _, it := range types {
		if *queryType == "" || *queryType == it.String() {
			t.Logf("Testing: --querytype=%s ...", it)
			testQueryType(t, fn, it)
		}
	}
}

func testQueryType(t *testing.T, fn func(*queryTest), itype indexType) {
	defer index.SetVerboseCorpusLogging(true)
	index.SetVerboseCorpusLogging(false)

	idx := index.NewMemoryIndex() // string key-value pairs in memory, as if they were on disk
	var err error
	var corpus *index.Corpus
	if itype == indexCorpusBuild {
		if corpus, err = idx.KeepInMemory(); err != nil {
			t.Fatal(err)
		}
	}
	qt := &queryTest{
		t:  t,
		id: indextest.NewIndexDeps(idx),
	}
	qt.id.Fataler = t
	qt.Handler = func() *Handler {
		h := NewHandler(idx, qt.id.SignerBlobRef)
		if itype == indexCorpusScan {
			if corpus, err = idx.KeepInMemory(); err != nil {
				t.Fatal(err)
			}
			idx.PreventStorageAccessForTesting()
		}
		if corpus != nil {
			h.SetCorpus(corpus)
		}
		return h
	}
	fn(qt)
}

func dumpRes(t *testing.T, res *SearchResult) {
	t.Logf("Got: %#v", res)
	for i, got := range res.Blobs {
		t.Logf(" %d. %s", i, got)
	}
}

func (qt *queryTest) wantRes(req *SearchQuery, wanted ...blob.Ref) {
	res, err := qt.Handler().Query(req)
	if err != nil {
		qt.t.Fatal(err)
	}

	need := make(map[blob.Ref]bool)
	for _, br := range wanted {
		need[br] = true
	}
	for _, bi := range res.Blobs {
		if !need[bi.Blob] {
			qt.t.Errorf("unexpected search result: %v", bi.Blob)
		} else {
			delete(need, bi.Blob)
		}
	}
	for br := range need {
		qt.t.Errorf("missing from search result: %v", br)
	}
}

func TestQuery(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		fileRef, wholeRef := qt.id.UploadFile("file.txt", "the content", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				Anything: true,
			},
			Limit: 0,
			Sort:  UnspecifiedSort,
		}
		qt.wantRes(sq, fileRef, wholeRef)
	})
}

func TestQueryCamliType(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		fileRef, _ := qt.id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		sq := &SearchQuery{
			Constraint: &Constraint{
				CamliType: "file",
			},
		}
		qt.wantRes(sq, fileRef)
	})
}

func TestQueryAnyCamliType(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		fileRef, _ := qt.id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				AnyCamliType: true,
			},
		}
		qt.wantRes(sq, fileRef)
	})
}

func TestQueryBlobSize(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		_, smallFileRef := qt.id.UploadFile("file.txt", strings.Repeat("x", 5<<10), time.Unix(1382073153, 0))
		qt.id.UploadFile("file.txt", strings.Repeat("x", 20<<10), time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				BlobSize: &IntConstraint{
					Min: 4 << 10,
					Max: 6 << 10,
				},
			},
		}
		qt.wantRes(sq, smallFileRef)
	})
}

func TestQueryBlobRefPrefix(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// foo is 0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33
		id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		// "bar.." is 08ef767ba2c93f8f40902118fa5260a65a2a4975
		id.UploadFile("file.txt", "bar..", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				BlobRefPrefix: "sha1-0",
			},
		}
		sres, err := qt.Handler().Query(sq)
		if err != nil {
			t.Fatal(err)
		}
		if len(sres.Blobs) < 2 {
			t.Errorf("expected at least 2 matches; got %d", len(sres.Blobs))
		}
		for _, res := range sres.Blobs {
			brStr := res.Blob.String()
			if !strings.HasPrefix(brStr, "sha1-0") {
				t.Errorf("matched blob %s didn't begin with sha1-0", brStr)
			}
		}
	})
}

func TestQueryTwoConstraints(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id
		id.UploadString("a")      // 86f7e437faa5a7fce15d1ddcb9eaeaea377667b8
		b := id.UploadString("b") // e9d71f5ee7c92d6dc9e92ffdad17b8bd49418f98
		id.UploadString("c4")     // e4666a670f042877c67a84473a71675ee0950a08

		sq := &SearchQuery{
			Constraint: &Constraint{
				BlobRefPrefix: "sha1-e", // matches b and c4
				BlobSize: &IntConstraint{ // matches a and b
					Min: 1,
					Max: 1,
				},
			},
		}
		qt.wantRes(sq, b)
	})
}

func TestQueryLogicalOr(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// foo is 0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33
		_, foo := id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		// "bar.." is 08ef767ba2c93f8f40902118fa5260a65a2a4975
		_, bar := id.UploadFile("file.txt", "bar..", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				Logical: &LogicalConstraint{
					Op: "or",
					A: &Constraint{
						BlobRefPrefix: "sha1-0beec7b5ea3f0fdbc95d0dd",
					},
					B: &Constraint{
						BlobRefPrefix: "sha1-08ef767ba2c93f8f40",
					},
				},
			},
		}
		qt.wantRes(sq, foo, bar)
	})
}

func TestQueryLogicalAnd(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// foo is 0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33
		_, foo := id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		// "bar.." is 08ef767ba2c93f8f40902118fa5260a65a2a4975
		id.UploadFile("file.txt", "bar..", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				Logical: &LogicalConstraint{
					Op: "and",
					A: &Constraint{
						BlobRefPrefix: "sha1-0",
					},
					B: &Constraint{
						BlobSize: &IntConstraint{
							Max: int64(len("foo")), // excludes "bar.."
						},
					},
				},
			},
		}
		qt.wantRes(sq, foo)
	})
}

func TestQueryLogicalXor(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// foo is 0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33
		_, foo := id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		// "bar.." is 08ef767ba2c93f8f40902118fa5260a65a2a4975
		id.UploadFile("file.txt", "bar..", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				Logical: &LogicalConstraint{
					Op: "xor",
					A: &Constraint{
						BlobRefPrefix: "sha1-0",
					},
					B: &Constraint{
						BlobRefPrefix: "sha1-08ef767ba2c93f8f40",
					},
				},
			},
		}
		qt.wantRes(sq, foo)
	})
}

func TestQueryLogicalNot(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// foo is 0beec7b5ea3f0fdbc95d0dd47f3c5bc275da8a33
		_, foo := id.UploadFile("file.txt", "foo", time.Unix(1382073153, 0))
		// "bar.." is 08ef767ba2c93f8f40902118fa5260a65a2a4975
		_, bar := id.UploadFile("file.txt", "bar..", time.Unix(1382073153, 0))

		sq := &SearchQuery{
			Constraint: &Constraint{
				Logical: &LogicalConstraint{
					Op: "not",
					A: &Constraint{
						CamliType: "file",
					},
				},
			},
		}
		qt.wantRes(sq, foo, bar)
	})
}

func TestQueryPermanodeAttrExact(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		p1 := id.NewPlannedPermanode("1")
		p2 := id.NewPlannedPermanode("2")
		id.SetAttribute(p1, "someAttr", "value1")
		id.SetAttribute(p2, "someAttr", "value2")

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr:  "someAttr",
					Value: "value1",
				},
			},
		}
		qt.wantRes(sq, p1)
	})
}

func TestQueryPermanodeAttrMatches(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		p1 := id.NewPlannedPermanode("1")
		p2 := id.NewPlannedPermanode("2")
		p3 := id.NewPlannedPermanode("3")
		id.SetAttribute(p1, "someAttr", "value1")
		id.SetAttribute(p2, "someAttr", "value2")
		id.SetAttribute(p3, "someAttr", "NOT starting with value")

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr: "someAttr",
					ValueMatches: &StringConstraint{
						HasPrefix: "value",
					},
				},
			},
		}
		qt.wantRes(sq, p1, p2)
	})
}

func TestQueryPermanodeAttrNumValue(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// TODO(bradfitz): if we set an empty attribute value here and try to search
		// by NumValue IntConstraint Min = 1, it fails only in classic (no corpus) mode.
		// Something there must be skipping empty values.
		p1 := id.NewPlannedPermanode("1")
		id.AddAttribute(p1, "x", "1")
		id.AddAttribute(p1, "x", "2")
		p2 := id.NewPlannedPermanode("2")
		id.AddAttribute(p2, "x", "1")
		id.AddAttribute(p2, "x", "2")
		id.AddAttribute(p2, "x", "3")

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr: "x",
					NumValue: &IntConstraint{
						Min: 3,
					},
				},
			},
		}
		qt.wantRes(sq, p2)
	})
}

// find a permanode (p2) that has a property being a blobref pointing
// to a sub-query
func TestQueryPermanodeAttrValueInSet(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		p1 := id.NewPlannedPermanode("1")
		id.SetAttribute(p1, "bar", "baz")
		p2 := id.NewPlannedPermanode("2")
		id.SetAttribute(p2, "foo", p1.String())

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr: "foo",
					ValueInSet: &Constraint{
						Permanode: &PermanodeConstraint{
							Attr:  "bar",
							Value: "baz",
						},
					},
				},
			},
		}
		qt.wantRes(sq, p2)
	})
}

// find permanodes matching a certain file query
func TestQueryFileConstraint(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id
		fileRef, _ := id.UploadFile("some-stuff.txt", "hello", time.Unix(123, 0))
		qt.t.Logf("fileRef = %q", fileRef)
		p1 := id.NewPlannedPermanode("1")
		id.SetAttribute(p1, "camliContent", fileRef.String())

		fileRef2, _ := id.UploadFile("other-file", "hellooooo", time.Unix(456, 0))
		qt.t.Logf("fileRef2 = %q", fileRef2)
		p2 := id.NewPlannedPermanode("2")
		id.SetAttribute(p2, "camliContent", fileRef2.String())

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr: "camliContent",
					ValueInSet: &Constraint{
						File: &FileConstraint{
							FileName: &StringConstraint{
								Contains: "-stuff",
							},
							FileSize: &IntConstraint{
								Max: 5,
							},
						},
					},
				},
			},
		}
		qt.wantRes(sq, p1)
	})
}

func TestQueryPermanodeModtime(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		// indextest advances time one second per operation:
		p1 := id.NewPlannedPermanode("1")
		p2 := id.NewPlannedPermanode("2")
		p3 := id.NewPlannedPermanode("3")
		id.SetAttribute(p1, "someAttr", "value1") // 2011-11-28 01:32:37.000123456 +0000 UTC 1322443957
		id.SetAttribute(p2, "someAttr", "value2") // 2011-11-28 01:32:38.000123456 +0000 UTC 1322443958
		id.SetAttribute(p3, "someAttr", "value3") // 2011-11-28 01:32:39.000123456 +0000 UTC 1322443959

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					ModTime: &TimeConstraint{
						After:  time.Unix(1322443957, 456789),
						Before: time.Unix(1322443959, 0),
					},
				},
			},
		}
		qt.wantRes(sq, p2)
	})
}

// This really belongs in pkg/index for the index-vs-corpus tests, but
// it's easier here for now.
// TODO: make all the indextest/tests.go
// also test the three memory build modes that testQuery does.
func TestDecodeFileInfo(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id
		fileRef, _ := id.UploadFile("file.gif", "GIF87afoo", time.Unix(456, 0))
		res, err := qt.Handler().Describe(&DescribeRequest{
			BlobRef: fileRef,
		})
		if err != nil {
			qt.t.Error(err)
			return
		}
		db := res.Meta[fileRef.String()]
		if db == nil {
			qt.t.Error("DescribedBlob missing")
			return
		}
		if db.File == nil {
			qt.t.Error("DescribedBlob.File is nil")
			return
		}
		if db.File.MIMEType != "image/gif" {
			qt.t.Error("DescribedBlob.File = %+v; mime type is not image/gif", db.File)
			return
		}
	})
}

func TestQueryRecentPermanodes(t *testing.T) {
	// TODO: care about classic (allIndexTypes) too?
	testQueryTypes(t, memIndexTypes, func(qt *queryTest) {
		id := qt.id

		p1 := id.NewPlannedPermanode("1")
		id.SetAttribute(p1, "foo", "p1")
		p2 := id.NewPlannedPermanode("2")
		id.SetAttribute(p2, "foo", "p2")
		p3 := id.NewPlannedPermanode("3")
		id.SetAttribute(p3, "foo", "p3")

		req := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{},
			},
			Limit:    2,
			Sort:     UnspecifiedSort,
			Describe: &DescribeRequest{},
		}
		res, err := qt.Handler().Query(req)
		if err != nil {
			qt.t.Fatal(err)
		}
		if s := ExportCandSource(); s != "corpus_permanode_desc" {
			t.Errorf("used candidate source strategy %q; want corpus_permanode_desc", s)
		}
		wantBlobs := []*SearchResultBlob{
			{Blob: p3},
			{Blob: p2},
		}
		if !reflect.DeepEqual(res.Blobs, wantBlobs) {
			gotj, wantj := prettyJSON(res.Blobs), prettyJSON(wantBlobs)
			t.Errorf("Got blobs:\n%s\nWant:\n%s\n", gotj, wantj)
		}
		if got := len(res.Describe.Meta); got != 2 {
			t.Errorf("got %d described blobs; want 2", got)
		}
	})
}

// Tests PermanodeConstraint.ValueAll
func TestQueryPermanodeValueAll(t *testing.T) {
	testQuery(t, func(qt *queryTest) {
		id := qt.id

		p1 := id.NewPlannedPermanode("1")
		p2 := id.NewPlannedPermanode("2")
		id.SetAttribute(p1, "attr", "foo")
		id.SetAttribute(p1, "attr", "barrrrr")
		id.SetAttribute(p2, "attr", "foo")
		id.SetAttribute(p2, "attr", "bar")

		sq := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{
					Attr:     "attr",
					ValueAll: true,
					ValueMatches: &StringConstraint{
						ByteLength: &IntConstraint{
							Min: 3,
							Max: 3,
						},
					},
				},
			},
		}
		qt.wantRes(sq, p2)
	})
}

// permanodes tagged "foo" or those in sets where the parent
// permanode set itself is tagged "foo".
func TestQueryPermanodeTaggedViaParent(t *testing.T) {
	t.Skip("TODO: finish implementing")

	testQuery(t, func(qt *queryTest) {
		id := qt.id

		ptagged := id.NewPlannedPermanode("tagged_photo")
		pindirect := id.NewPlannedPermanode("via_parent")
		pset := id.NewPlannedPermanode("set")
		pboth := id.NewPlannedPermanode("both") // funny directly and via its parent
		pnotfunny := id.NewPlannedPermanode("not_funny")

		id.SetAttribute(ptagged, "tag", "funny")
		id.SetAttribute(pset, "tag", "funny")
		id.SetAttribute(pboth, "tag", "funny")
		id.AddAttribute(pset, "camliMember", pindirect.String())
		id.AddAttribute(pset, "camliMember", pboth.String())
		id.SetAttribute(pnotfunny, "tag", "boring")

		sq := &SearchQuery{
			Constraint: &Constraint{
				Logical: &LogicalConstraint{
					Op: "or",

					// Those tagged funny directly:
					A: &Constraint{
						Permanode: &PermanodeConstraint{
							Attr:  "tag",
							Value: "funny",
						},
					},

					// Those tagged funny indirectly:
					B: &Constraint{
						Permanode: &PermanodeConstraint{
							Relation: &RelationConstraint{
								Relation: "ancestor", // "parent", "child", "progeny"
								// Counter-part to "Any" is "All". Only one may be set.
								Any: &Constraint{
									Permanode: &PermanodeConstraint{
										Attr:  "tag",
										Value: "funny",
									},
								},
							},
						},
					},
				},
			},
		}
		qt.wantRes(sq, ptagged, pset, pboth, pindirect)
	})
}

func TestLimitDoesntDeadlock(t *testing.T) {
	// TODO: care about classic (allIndexTypes) too?
	testQueryTypes(t, memIndexTypes, func(qt *queryTest) {
		id := qt.id

		const limit = 2
		for i := 0; i < ExportBufferedConst()+limit+1; i++ {
			pn := id.NewPlannedPermanode(fmt.Sprint(i))
			id.SetAttribute(pn, "foo", "bar")
		}

		req := &SearchQuery{
			Constraint: &Constraint{
				Permanode: &PermanodeConstraint{},
			},
			Limit:    limit,
			Sort:     UnspecifiedSort,
			Describe: &DescribeRequest{},
		}
		h := qt.Handler()
		gotRes := make(chan bool, 1)
		go func() {
			_, err := h.Query(req)
			if err != nil {
				qt.t.Error(err)
			}
			gotRes <- true
		}()
		select {
		case <-gotRes:
		case <-time.After(5 * time.Second):
			t.Error("timeout; deadlock?")
		}
	})
}

func prettyJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestPlannedQuery(t *testing.T) {
	tests := []struct {
		in, want *SearchQuery
	}{
		{
			in: &SearchQuery{
				Constraint: &Constraint{
					Permanode: &PermanodeConstraint{},
				},
			},
			want: &SearchQuery{
				Sort: LastModifiedDesc,
				Constraint: &Constraint{
					Permanode: &PermanodeConstraint{},
				},
				Limit: 200,
			},
		},
	}
	for i, tt := range tests {
		got := tt.in.ExportPlannedQuery()
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%d. for input:\n%s\ngot:\n%s\nwant:\n%s\n", i,
				prettyJSON(tt.in), prettyJSON(got), prettyJSON(tt.want))
		}
	}
}
