package main

import (
	bittorrent "github.com/anacrolix/torrent"
	"testing"
)

func TestPartialSharingEligibility(t *testing.T) {
	complete := bittorrent.PieceStateRun{Length: 1}
	complete.Complete = true
	incomplete := bittorrent.PieceStateRun{Length: 9}
	incomplete.Partial = true
	if !isSharing(true, bittorrent.PieceStateRuns{complete, incomplete}) {
		t.Fatal("a partial torrent with a verified piece must count as sharing")
	}
	if isSharing(true, bittorrent.PieceStateRuns{incomplete}) {
		t.Fatal("unfinished pieces cannot be shared")
	}
	if isSharing(false, bittorrent.PieceStateRuns{complete}) {
		t.Fatal("disabled uploads must not earn sharing time")
	}
	complete.Hashing = true
	if isSharing(true, bittorrent.PieceStateRuns{complete}) {
		t.Fatal("pieces still being verified must not earn sharing time")
	}
	if isSharing(true, nil) {
		t.Fatal("an empty torrent must not earn sharing time")
	}
}
