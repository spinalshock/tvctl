package device

import (
	"fmt"
	"strings"
)

// Problem is an error that knows how to be said. Say is the one sentence
// a listener hears (an assistant reads tvctl's plain output aloud); the
// notes are the rest, a cause or what to do next, kept for --json and
// logs. Every error tvctl produces itself is one, built with Fail, so the
// spoken form is written where the error happens, never guessed back out
// of a string.
type Problem struct {
	say   string
	notes []string
	cause error
}

// Fail starts a Problem with the sentence to say. Add context with Note
// and a cause with Because; both are optional.
func Fail(say string) *Problem { return &Problem{say: say} }

// Failf is Fail with formatting.
func Failf(format string, a ...any) *Problem { return &Problem{say: fmt.Sprintf(format, a...)} }

// Note adds a detail for the terminal and --json: a hint ("run tv pair"),
// an address, an HTTP status. Never spoken.
func (p *Problem) Note(note string) *Problem {
	if note != "" {
		p.notes = append(p.notes, note)
	}
	return p
}

// Notef is Note with formatting.
func (p *Problem) Notef(format string, a ...any) *Problem {
	return p.Note(fmt.Sprintf(format, a...))
}

// Because records the underlying error, for errors.Is and the notes.
func (p *Problem) Because(err error) *Problem {
	if err != nil {
		p.cause = err
		p.notes = append(p.notes, err.Error())
	}
	return p
}

// Said is the sentence to speak.
func (p *Problem) Said() string { return p.say }

// Error is the sentence followed by the notes in brackets.
func (p *Problem) Error() string {
	if len(p.notes) == 0 {
		return p.say
	}
	return p.say + " (" + strings.Join(p.notes, "; ") + ")"
}

func (p *Problem) Unwrap() error { return p.cause }
