package xml

import (
	"encoding/xml"
	"strings"
	"testing"
)

// Story-body cues in the shape a live ENPS sends them.
//
// Every fixture here is the structure of a real captured frame, with the editorial text replaced.
// The delimiters, the paragraph breaks and the empty backslash slots are verbatim, because those
// are the parts a parser gets wrong.

// A production command is bracketed text in the body, and it sits immediately after the MOS item
// it acts on -- captured as </storyItem><p>[TAKE SOT</p>.
func TestProductionCommandsAreExtractedFromBodyText(t *testing.T) {
	const frame = `<storyBody>` +
		`<p>Anchor reads this line.</p>` +
		`<p>[TAKE VO]</p>` +
		`<p>More script here.</p>` +
		`<p>[TAKE :FULLSCREEN]</p>` +
		`</storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %+v", len(cues), cues)
	}

	if cues[0].Kind != CueProduction {
		t.Errorf("kind = %q, want %q", cues[0].Kind, CueProduction)
	}
	if cues[0].Verb != "TAKE" || cues[0].Target != "VO" {
		t.Errorf("first cue = %q/%q, want TAKE/VO", cues[0].Verb, cues[0].Target)
	}
	if cues[0].Paragraph != 1 {
		t.Errorf("first cue paragraph = %d, want 1", cues[0].Paragraph)
	}

	// The leading colon is preserved. Some targets carry one and some do not, and nothing in
	// the captured traffic establishes what it means, so stripping it would be inventing
	// semantics.
	if cues[1].Target != ":FULLSCREEN" {
		t.Errorf("second cue target = %q, want :FULLSCREEN verbatim", cues[1].Target)
	}
}

// The defect this test exists for: ENPS breaks a command across a paragraph boundary, so the
// closing bracket and the parameter are in the NEXT <p>. Captured verbatim:
//
//	</storyItem><p>[TAKE SOT</p><p>DURATION:0:18]</p>
//
// 28 of 219 observed commands arrive this way. A parser that scans one paragraph at a time sees
// an unterminated '[', finds no command, and silently loses the duration. No paragraph in the
// captured corpus contains a newline of its own, so the paragraph break is the only separator.
func TestACommandSplitAcrossParagraphsKeepsItsParameter(t *testing.T) {
	const frame = `<storyBody>` +
		`<p> </p>` +
		`<p>[TAKE SOT</p>` +
		`<p>DURATION:0:18]</p>` +
		`</storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	if len(cues) != 1 {
		t.Fatalf("expected 1 cue, got %d: %+v", len(cues), cues)
	}
	if cues[0].Verb != "TAKE" || cues[0].Target != "SOT" {
		t.Errorf("cue = %q/%q, want TAKE/SOT", cues[0].Verb, cues[0].Target)
	}
	// A timecode value contains colons of its own; only the first splits key from value.
	if got := cues[0].Params["DURATION"]; got != "0:18" {
		t.Errorf("DURATION = %q, want 0:18", got)
	}
	if cues[0].Paragraph != 1 {
		t.Errorf("paragraph = %d, want 1 (where the command starts)", cues[0].Paragraph)
	}
}

// Serial CG: a legacy character-generator command, backslash-delimited. Two dialects appear on
// the same estate -- a named template, and the same thing routed through automation. Both must
// classify as serial CG or a consumer has to know the dialect before it can tell what it is.
func TestSerialCGCommandsAreRecognisedInBothDialects(t *testing.T) {
	const frame = `<storyBody>` +
		`<p>[CG :#Lower Third\Presenter Name\Second line]</p>` +
		`<p>[AUTOMATION:CG\00001\Presenter Name\Location\Third line\\\\\\AUTOMATIC]</p>` +
		`</storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %+v", len(cues), cues)
	}

	named := cues[0]
	if named.Kind != CueSerialCG {
		t.Errorf("named dialect kind = %q, want %q", named.Kind, CueSerialCG)
	}
	if named.Verb != "CG" || named.Target != ":#Lower Third" {
		t.Errorf("named dialect = %q/%q, want CG/:#Lower Third", named.Verb, named.Target)
	}
	if len(named.Fields) != 2 || named.Fields[0] != "Presenter Name" || named.Fields[1] != "Second line" {
		t.Errorf("named dialect fields = %q", named.Fields)
	}

	routed := cues[1]
	if routed.Kind != CueSerialCG {
		t.Errorf("automation dialect kind = %q, want %q", routed.Kind, CueSerialCG)
	}
	if routed.Verb != "AUTOMATION" || routed.Target != "CG" {
		t.Errorf("automation dialect = %q/%q, want AUTOMATION/CG", routed.Verb, routed.Target)
	}
	// Empty slots are unfilled template fields and hold position, exactly as the pipe-joined
	// itemSlug does (doc/interop §50). Compacting them would shift every later field.
	want := []string{"00001", "Presenter Name", "Location", "Third line", "", "", "", "", "", "AUTOMATIC"}
	if len(routed.Fields) != len(want) {
		t.Fatalf("automation fields = %d (%q), want %d", len(routed.Fields), routed.Fields, len(want))
	}
	for i := range want {
		if routed.Fields[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, routed.Fields[i], want[i])
		}
	}
}

// Prompter markers are the third class and are NOT machine commands. They pair one-for-one with
// the bracketed commands -- {***VO***} 52 times against [TAKE VO] 52 times -- and additionally
// carry the presenter's name, which has no command counterpart at all.
func TestPrompterMarkersAreClassifiedSeparately(t *testing.T) {
	const frame = `<storyBody>` +
		`<p>{***PRESENTER***}</p>` +
		`<p>[TAKE VO]</p>` +
		`<p>{***VO***}</p>` +
		`</storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	if len(cues) != 3 {
		t.Fatalf("expected 3 cues, got %d: %+v", len(cues), cues)
	}
	if cues[0].Kind != CuePrompter || cues[0].Target != "PRESENTER" {
		t.Errorf("first = %q/%q, want %s/PRESENTER", cues[0].Kind, cues[0].Target, CuePrompter)
	}
	if cues[1].Kind != CueProduction {
		t.Errorf("second kind = %q, want %q", cues[1].Kind, CueProduction)
	}
	if cues[2].Kind != CuePrompter || cues[2].Target != "VO" {
		t.Errorf("third = %q/%q, want %s/VO", cues[2].Kind, cues[2].Target, CuePrompter)
	}
	// Document order is preserved across the classes: the marker precedes its command here,
	// and a consumer sequencing playout depends on that order.
	if cues[0].Paragraph != 0 || cues[1].Paragraph != 1 || cues[2].Paragraph != 2 {
		t.Errorf("paragraphs = %d/%d/%d, want 0/1/2",
			cues[0].Paragraph, cues[1].Paragraph, cues[2].Paragraph)
	}
}

// An unterminated delimiter must not swallow the rest of the story. Brackets were always balanced
// in the captured corpus, but body text is author-entered and a stray '[' is a matter of time.
func TestAnUnterminatedDelimiterDoesNotConsumeTheBody(t *testing.T) {
	const frame = `<storyBody>` +
		`<p>Script with a stray [ bracket in it.</p>` +
		`<p>[TAKE VO]</p>` +
		`</storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	// The stray '[' finds the ']' of the real command, which is the only pairing available; the
	// requirement is that parsing continues and produces something rather than hanging or
	// discarding the body. What must NOT happen is a cue whose Raw runs past the closing
	// bracket.
	for _, c := range cues {
		if strings.Contains(c.Raw, "]") {
			t.Errorf("cue Raw ran past its closing delimiter: %q", c.Raw)
		}
	}
	if len(cues) == 0 {
		t.Fatal("expected parsing to continue past the stray bracket")
	}
}

// A body with no cues at all is the common case: most stories are script only.
func TestAScriptWithoutCuesYieldsNone(t *testing.T) {
	const frame = `<storyBody><p>Just script.</p><p> </p></storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cues := body.Cues(); len(cues) != 0 {
		t.Errorf("expected no cues, got %+v", cues)
	}
}

// The target is not always on the first line. Found in live traffic AFTER the first version of
// this parser shipped: a command can leave the inline target empty -- a bare ':' -- and carry the
// real target in a NAME parameter. So a cue whose Target parses as ":" is not malformed, and a
// consumer must not assume the first line names what to take (doc/interop §51).
func TestATargetCarriedInAParameterRatherThanInline(t *testing.T) {
	const frame = `<storyBody><p>[TAKE :</p><p>NAME:2XB]</p></storyBody>`

	var body StoryBody
	if err := xml.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cues := body.Cues()
	if len(cues) != 1 {
		t.Fatalf("expected 1 cue, got %d: %+v", len(cues), cues)
	}
	if cues[0].Verb != "TAKE" {
		t.Errorf("verb = %q, want TAKE", cues[0].Verb)
	}
	if cues[0].Target != ":" {
		t.Errorf("target = %q, want the bare colon preserved as sent", cues[0].Target)
	}
	if got := cues[0].Params["NAME"]; got != "2XB" {
		t.Errorf("NAME = %q, want 2XB", got)
	}
}

// A duration is whatever a human typed. Both forms occur in one rundown and nothing distinguishes
// them but the colon, so the value is carried verbatim rather than normalised into a unit the
// sender never committed to -- the same reasoning as roStatus being free prose.
func TestDurationValuesAreCarriedVerbatimInBothFormats(t *testing.T) {
	for _, want := range []string{"28", "0:18"} {
		frame := `<storyBody><p>[TAKE SOT</p><p>DURATION:` + want + `]</p></storyBody>`

		var body StoryBody
		if err := xml.Unmarshal([]byte(frame), &body); err != nil {
			t.Fatalf("unmarshal %q: %v", want, err)
		}
		cues := body.Cues()
		if len(cues) != 1 {
			t.Fatalf("%q: expected 1 cue, got %d", want, len(cues))
		}
		if got := cues[0].Params["DURATION"]; got != want {
			t.Errorf("DURATION = %q, want %q", got, want)
		}
	}
}
