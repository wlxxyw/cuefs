//go:build ignore

// Generates the multi-encoding cue fixtures under testdata/. Run from the
// repository root:
//
//	go run testdata/gen.go
//
// The fixtures are committed so that `cuefs probe` can be exercised by hand
// without having to manufacture encoded files first.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"cuefs/internal/cue"
)

const simplified = `REM DISCID A10A2E0D
PERFORMER "测试乐队"
TITLE "测试专辑"
CATALOG 5054196339524
REM DATE 2014
REM GENRE 摇滚乐
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    PERFORMER "测试乐队"
    TITLE "第一首歌"
    ISRC CN12345678901
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "测试乐队; 客串歌手"
    TITLE "第二首歌"
    INDEX 01 03:15:20
  TRACK 03 AUDIO
    TITLE "第三首歌"
    INDEX 01 07:44:05
`

const traditional = `REM DISCID B20A2E0D
PERFORMER "測試樂隊"
TITLE "測試專輯"
CATALOG 5054196339524
REM DATE 2014
REM GENRE 搖滾樂
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    PERFORMER "測試樂隊"
    TITLE "第一首曲目"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PERFORMER "測試樂隊; 客串歌手"
    TITLE "第二首曲目"
    INDEX 01 04:12:33
`

// eacStyle mimics what EAC/EAC30 writes: plain ASCII, unquoted values and
// plenty of REM lines.
const eacStyle = `REM GENRE Rock
REM DATE 1998
REM DISCID 9C0A1110
REM COMMENT "ExactAudioCopy v1.6"
REM TOTALDISCS 1
PERFORMER "Some Band"
TITLE "Some Album"
FILE "Some Album.wav" WAVE
  TRACK 01 AUDIO
    TITLE "Opening"
    PERFORMER "Some Band"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Second Song"
    PERFORMER "Some Band"
    INDEX 01 04:33:12
  TRACK 03 AUDIO
    TITLE "Encore"
    PERFORMER "Some Band"
    INDEX 01 09:02:44
`

func main() {
	out := []struct {
		name string
		text string
		enc  string
	}{
		{"utf8.cue", simplified, "utf-8"},
		{"gbk.cue", simplified, "gbk"},
		{"big5.cue", traditional, "big5"},
		{"eac-ascii.cue", eacStyle, "utf-8"},
		{"shiftjis.cue", japanese(), "shift_jis"},
	}
	for _, f := range out {
		var b []byte
		var err error
		if f.enc == "utf-8" {
			b = []byte(f.text)
		} else {
			b, err = encode(f.enc, f.text)
			if err != nil {
				fmt.Fprintln(os.Stderr, f.name, err)
				os.Exit(1)
			}
		}
		p := filepath.Join("testdata", f.name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, p, err)
			os.Exit(1)
		}
		fmt.Println("wrote", p, len(b), "bytes")
	}
}

func japanese() string {
	return `REM DISCID C30A2E0D
PERFORMER "テストバンド"
TITLE "テストアルバム"
FILE "album.flac" WAVE
  TRACK 01 AUDIO
    TITLE "一曲目"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "二曲目"
    INDEX 01 04:12:33
`
}

func encode(name, text string) ([]byte, error) {
	return cue.Encode(name, text)
}
