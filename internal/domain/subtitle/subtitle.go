// Package subtitle builds the ASS document ffmpeg's own mov_text encoder
// converts into a soft MP4 subtitle track (FR-021), and maps a script
// language to the ISO 639-2/T tag the track is stored under. It is pure: no
// I/O, no subprocess, like the rest of domain (CODE_QUALITY separation of
// concerns).
package subtitle

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// Cue is one narrated step's text, shown from Start to End in the output
// video (after the intro, same timeline as the narration mix).
type Cue struct {
	Start, End time.Duration
	Text       string
}

// Style is the validated per-script style. A zero field keeps ffmpeg's own
// SRT→mov_text default for that attribute, pinned against this repo's
// ffmpeg (5.1.9) by the spike: Font "Arial", Size 16, Color white, Background
// opaque black (ARCHITECTURE §17.5-style spike note, FR-021).
type Style struct {
	Font       string // e.g. "Arial"; "" = ffmpeg default (Arial)
	Size       int    // px; 0 = ffmpeg default (16)
	Color      string // "#RRGGBB"; "" = ffmpeg default (white)
	Background string // "#RRGGBB" or "#RRGGBBAA"; "" = ffmpeg default (opaque black)
}

// defaultFont and defaultSize are ffmpeg's own bare-SRT→mov_text defaults,
// used verbatim when Style leaves Font/Size unset, so plain `subtitles: true`
// looks like a bare SRT (spike, step 1).
const (
	defaultFont = "Arial"
	defaultSize = 16
)

// defaultPrimary and defaultBack are the ASS colours that reproduce ffmpeg's
// own bare-SRT defaults (white text, opaque black background), byte-verified
// against ffmpeg's bare .srt→mov_text output in the spike.
const (
	defaultPrimary = "&H00FFFFFF"
	defaultBack    = "&H00000000"
)

// assTemplate is the one Default style every ASS document uses. PlayResX and
// PlayResY are fixed at the video's own resolution (NFR-002); fields beyond
// font, size and colour (bold, outline, alignment, margins, ...) are never
// carried into the mov_text track (confirmed by the spike: the tx3g sample
// description has no position, outline or alignment field at all), so they
// stay at one constant, unexposed value (trade-off, see spec).
const assTemplate = `[Script Info]
ScriptType: v4.00+
PlayResX: 1920
PlayResY: 1080

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,%s,%d,%s,&H000000FF,&H00000000,%s,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`

// ASS renders the ASS document ffmpeg converts to mov_text: one "Default"
// style built from st, one Dialogue line per cue, text escaped so narration
// cannot inject an ASS override tag or a hard line/space break (spike,
// step 1).
func ASS(cues []Cue, st Style) []byte {
	font, size := defaultFont, defaultSize
	if st.Font != "" {
		font = st.Font
	}
	if st.Size != 0 {
		size = st.Size
	}
	primary, back := defaultPrimary, defaultBack
	if st.Color != "" {
		primary = assColor(st.Color)
	}
	if st.Background != "" {
		back = assColor(st.Background)
	}

	var b strings.Builder
	fmt.Fprintf(&b, assTemplate, font, size, primary, back)
	for _, c := range cues {
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", assTime(c.Start), assTime(c.End), escape(c.Text))
	}
	return []byte(b.String())
}

// assColor converts "#RRGGBB" or "#RRGGBBAA" to an ASS &HAABBGGRR colour. ASS
// alpha is inverted from the usual convention (00 = fully opaque), and AA
// defaults to FF (opaque) when not given. Byte order and the inversion are
// pinned by the spike against this repo's ffmpeg: #RRGGBB(AA) round-trips to
// the tx3g sample description's RGBA colour unchanged.
func assColor(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	rr, gg, bb, aa := hex[0:2], hex[2:4], hex[4:6], "FF"
	if len(hex) == 8 {
		aa = hex[6:8]
	}
	alpha, _ := strconv.ParseUint(aa, 16, 8)
	return fmt.Sprintf("&H%02X%s%s%s", 255-alpha, bb, gg, rr)
}

// assTime formats d as ASS's H:MM:SS.cc, centiseconds rounded from d.
func assTime(d time.Duration) string {
	cs := int64((d + 5*time.Millisecond) / (10 * time.Millisecond)) // round to the nearest centisecond
	h := cs / (100 * 60 * 60)
	m := (cs / (100 * 60)) % 60
	s := (cs / 100) % 60
	c := cs % 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, c)
}

// escape neutralizes narration text before it goes into an ASS Dialogue
// line (spike, step 1): `{`/`}` are replaced so narration can never open an
// ASS override block (a recognized tag inside one, e.g. "{\b1}", is stripped
// and style-run-ed by ffmpeg's own mov_text encoder); every backslash is
// dropped, because `\N`/`\n`/`\h` are hard/soft-break and hard-space escapes
// even outside an override block, and a literal backslash is otherwise never
// meaningful in spoken narration; our own newline becomes the literal two
// characters `\N`, ASS's hard line break.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			// dropped: see the function comment.
		case '{':
			b.WriteByte('(')
		case '}':
			b.WriteByte(')')
		case '\r':
			// dropped: only \n carries the line break.
		case '\n':
			b.WriteString(`\N`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Lang maps an ISO 639-1 code (as used by script.languages and Piper voice
// names) to the ISO 639-2/T tag ffmpeg keeps in the mov_text stream, via
// golang.org/x/text/language (already an indirect dependency, promoted to
// direct by this feature, over a second hand-written table: CODE_QUALITY
// DRY). An unparseable code gives "und", the tag ffmpeg itself uses for an
// unknown language.
func Lang(iso1 string) string {
	tag, err := language.Parse(iso1)
	if err != nil {
		return "und"
	}
	base, conf := tag.Base()
	if conf == language.No {
		return "und"
	}
	return base.ISO3()
}
