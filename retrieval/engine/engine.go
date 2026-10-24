// Package engine exposes the same rebuildable retrieval engine used by re-search.
package engine

import "github.com/adiazpar/re-discipline/retrieval/internal/search"

type Hit = search.Hit
type Options = search.QueryOptions
type Doc = search.Doc
type SymbolHits = search.SymbolHits

var Query = search.QueryOpts
var QuerySnapshot = search.QuerySnapshot
var QuerySnapshotContext = search.QuerySnapshotContext
var Parse = search.ParseDoc
var Build = search.BuildIndexFile
var BuildContext = search.BuildIndexFileContext
var Symbols = search.LookupSymbol
var TryLock = search.TryLock
