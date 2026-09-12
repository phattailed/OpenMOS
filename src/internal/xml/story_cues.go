package xml

import "strings"

// Non-MOS instructions carried as plain text inside the story body.
//
// A newsroom system can drive equipment that has no MOS device of its own: legacy serial character
// generators, and production commands read by automation or by a human director. ENPS carries those
// as ordinary body text, so from the NCS's point of view they are indistinguishable from the script.
// They arrive ONLY in roStorySend, because that is the only message carrying a story body -- a
// roList describes structure and never reaches this content.
//
// Measured across 449 roStorySend frames from a live ENPS (doc/interop §51):
//
//	219  [ ... ]      bracketed commands
//	351  { *** ... }  prompter markers
//	  0  <pi>         the specification's production-instruction element, never used by this NCS
//
// The specification does define <pi> for exactly this purpose and StoryParagraph models it, but
// this estate emits bracketed text instead. Both are supported; only one is populated in practice.
const (
	// CueProduction is a machine or director instruction: [TAKE VO], [TAKE :FULLSCREEN],
	// or [TAKE SOT] carrying a DURATION parameter.
	CueProduction = "PRODUCTION"
	// CueSerialCG is a legacy serial character-generator command, recognised by its
	// backslash-delimited field list: [CG :#Lower Third\Line one\Line two].
	CueSerialCG = "SERIAL_CG"
	// CuePrompter is a prompter-visible marker: {***VO***}. Not a machine command -- these
	// pair one-for-one with the bracketed commands ({***VO***} 52 times against [TAKE VO] 52
	// times) and additionally name the presenter.
	CuePrompter = "PROMPTER"
)

// BodyCue is one instruction extracted from a story body.
//
// Raw is kept verbatim alongside the parsed fields for the same reason mosExternalMetadata is:
// the grammar is a vendor convention rather than a specified format, so a consumer must be able
// to fall back to the original text when the parse does not carry enough meaning.
type BodyCue struct {
	// Kind is CueProduction, CueSerialCG or CuePrompter.
	Kind string `json:"kind"`
	// Raw is the text between the delimiters, exactly as it arrived, newlines included.
	Raw string `json:"raw"`
	// Verb is the leading token, upper-cased: TAKE, CG or AUTOMATION.
	Verb string `json:"verb,omitempty"`
	// Target is what the verb acts on. Preserved verbatim INCLUDING any leading ':' or '#',
	// which appear on some targets (:ANIMATION, :#Lower Third) and not others (VO, PKG,
	// SOT). Their meaning is not established from the captures, so they are not stripped.
	Target string `json:"target,omitempty"`
	// Fields holds the backslash-delimited values of a serial CG command, in template order,
	// empties included. Empty slots are significant: they are unfilled template fields, the
	// same positional convention the MOS itemSlug uses (doc/interop §50).
	Fields []string `json:"fields,omitempty"`
	// Params holds KEY:VALUE lines that follow the command on subsequent lines, keyed
	// upper-case. The only one observed is DURATION.
	Params map[string]string `json:"params,omitempty"`
	// Paragraph is the index of the body paragraph the cue STARTS in. A command may span a
	// paragraph boundary; see Cues.
	Paragraph int `json:"paragraph"`
}

// Cues extracts the non-MOS instructions from the body, in document order.
//
// This is a method on StoryBody rather than a free function taking paragraph text, because a
// field declared and never referenced is the defect this repository has now found five times
// (doc/interop §§40, 49, 51). A caller holding a StoryBody can reach the cues without knowing
// they are assembled from paragraph text.
//
// The paragraphs are JOINED BEFORE SCANNING, which is load-bearing. 28 of 219 observed commands
// span a paragraph boundary -- ENPS breaks the line inside the brackets:
//
//	<p>[TAKE SOT</p>
//	<p>DURATION:0:18]</p>
//
// Scanning paragraph by paragraph would find an unterminated '[', discard the parameter, and
// leave the duration behind. Nothing in the frame marks the continuation; only the unbalanced
// delimiter reveals it.
//
// An unterminated delimiter is skipped rather than guessed at, so a stray bracket in the script
// cannot swallow the rest of the story. Across the captured corpus brackets were always balanced
// and never nested, and no command shared a paragraph with prose.
func (b StoryBody) Cues() []BodyCue {
	if len(b.Paragraphs) == 0 {
		return nil
	}

	var sb strings.Builder
	starts := make([]int, len(b.Paragraphs))
	for i, p := range b.Paragraphs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		starts[i] = sb.Len()
		sb.WriteString(p.Content)
	}
	text := sb.String()

	paragraphAt := func(offset int) int {
		idx := 0
		for i, start := range starts {
			if start > offset {
				break
			}
			idx = i
		}
		return idx
	}

	var cues []BodyCue
	scanBodyCues(text, func(start, end int, cue BodyCue) {
		cue.Paragraph = paragraphAt(start)
		cues = append(cues, cue)
	})
	return cues
}

func scanBodyCues(text string, emit func(start, end int, cue BodyCue)) {
	for i := 0; i < len(text); i++ {
		var closer byte
		prompter := false
		switch text[i] {
		case '[':
			closer = ']'
		case '{':
			closer, prompter = '}', true
		default:
			continue
		}

		rel := strings.IndexByte(text[i+1:], closer)
		if rel < 0 {
			continue
		}
		cue := BodyCue{Raw: text[i+1 : i+1+rel]}
		if prompter {
			cue.Kind = CuePrompter
			cue.Target = strings.TrimSpace(strings.Trim(strings.TrimSpace(cue.Raw), "*"))
		} else {
			cue.parseCommand()
		}
		emit(i, i+rel+2, cue)
		i += rel + 1
	}
}

// parseCommand splits a bracketed command into verb, target, fields and parameters.
//
// Three shapes appear in live traffic, and the delimiter distinguishes them:
//
//	[TAKE VO]                                     verb + target
//	[TAKE SOT\nDURATION:0:18]                     verb + target + parameter lines
//	[CG :#Lower Third\Presenter Name\Saw it happen]  verb + target + backslash fields
//	[AUTOMATION:CG\00001\Presenter Name\...\AUTOMATIC]  automation routing to a named device
//
// A backslash is what marks a serial CG command: it never appeared in any other command in the
// captured corpus. Classifying on the delimiter rather than on the verb keeps a CG command
// recognisable whether it arrives as "CG" or routed through "AUTOMATION:CG".
func (c *BodyCue) parseCommand() {
	lines := strings.Split(c.Raw, "\n")
	head := strings.TrimSpace(lines[0])

	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if c.Params == nil {
			c.Params = make(map[string]string)
		}
		// Values may themselves contain a colon: DURATION:0:18 is a timecode, not a nested key.
		c.Params[key] = strings.TrimSpace(value)
	}

	// AUTOMATION:<device>\<field>... routes to a device named after the colon.
	if verb, rest, ok := strings.Cut(head, ":"); ok && strings.EqualFold(strings.TrimSpace(verb), "AUTOMATION") {
		c.Verb = "AUTOMATION"
		parts := strings.Split(rest, `\`)
		c.Target = strings.TrimSpace(parts[0])
		c.Fields = parts[1:]
		c.Kind = CueProduction
		if strings.EqualFold(c.Target, "CG") {
			c.Kind = CueSerialCG
		}
		return
	}

	verb, rest, _ := strings.Cut(head, " ")
	c.Verb = strings.ToUpper(strings.TrimSpace(verb))
	rest = strings.TrimSpace(rest)

	if strings.Contains(rest, `\`) {
		parts := strings.Split(rest, `\`)
		c.Target = strings.TrimSpace(parts[0])
		c.Fields = parts[1:]
		c.Kind = CueSerialCG
		return
	}

	c.Target = rest
	c.Kind = CueProduction
}
