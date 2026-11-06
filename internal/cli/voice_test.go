package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spinalshock/tvctl/internal/config"
	"github.com/spinalshock/tvctl/internal/device"
)

// Errors are written for terminals; the spoken form is the first clause,
// capitalised, without the hint about what to run.
func TestSpoken(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		// Our own errors say exactly their sentence; notes stay in --json.
		{device.Fail("not paired with the LG").Note("run tv pair"), "Not paired with the LG."},
		{device.Failf("%s is off or unreachable", "webOS TV UR8040PSB").Note("192.168.29.76").Because(errors.New("dial tcp: no route to host")), "webOS TV UR8040PSB is off or unreachable."},
		{device.Fail("no TVs found").Notef("pass the IP: tv pair %s", "10.0.0.1"), "No TVs found."},
		{device.Failf("found %d TVs, say which: %s", 2, "LG (10.0.0.1), Fire (10.0.0.2)"), "Found 2 TVs, say which: LG (10.0.0.1), Fire (10.0.0.2)."},
		{device.Fail("the TV didn't answer").Note("was the prompt accepted?"), "The TV didn't answer."},
		{fmt.Errorf("verb failed: %w", device.Fail("nothing on YouTube for xyz")), "Nothing on YouTube for xyz."},
		// Foreign errors: first clause only.
		{errors.New("dial tcp 10.0.0.5:8080: connect: no route to host"), "Dial tcp 10.0.0.5:8080."},
		{errors.New("accepts 1 arg(s), received 0"), "Accepts 1 arg(s), received 0."},
	}
	for _, c := range cases {
		if got := spoken(c.err); got != c.want {
			t.Errorf("spoken(%v)\n got %q\nwant %q", c.err, got, c.want)
		}
	}
	// The terminal and --json keep the notes.
	if got := device.Fail("not paired with the LG").Note("run tv pair").Error(); got != "not paired with the LG (run tv pair)" {
		t.Errorf("Error(): %q", got)
	}
	if sentence("") != "" {
		t.Error("empty sentence")
	}
	if got := spokenInput("hdmi2"); got != "HDMI 2" {
		t.Errorf("spokenInput: %q", got)
	}
	if got := spokenInput("PS5"); got != "PS5" {
		t.Errorf("spokenInput label: %q", got)
	}
}

// The manifest is the command tree: every verb is a tool, the companion-app
// ones say so, streaming ones are marked blocking, and quick ones carry
// their short deadline.
func TestToolsCoverVerbs(t *testing.T) {
	tools := map[string]Tool{}
	for _, tl := range Tools() {
		tools[tl.Name] = tl
	}
	for _, v := range Verbs() {
		if v == "tools" || v == "init" || v == "shell" || v == "install" {
			continue
		}
		if _, ok := tools[v]; !ok {
			t.Errorf("verb %q missing from tools", v)
		}
	}
	for _, name := range []string{"home", "home add", "home wallpaper", "overlay", "dash", "events", "shot", "watch"} {
		if tools[name].Needs != needsCompanion {
			t.Errorf("%q should need the companion app", name)
		}
	}
	for _, name := range []string{"key", "vol", "now", "toast", "input", "pair", "pin", "youtube", "play", "seek"} {
		if tools[name].Needs != "" {
			t.Errorf("%q should not need the companion app", name)
		}
	}
	for _, name := range []string{"events", "watch", "play"} {
		if !tools[name].Blocking {
			t.Errorf("%q should be blocking", name)
		}
	}
	if tools["key"].Deadline != "5s" || tools["apps"].Deadline != "15s" || tools["key"].Blocking {
		t.Errorf("deadlines: key=%+v apps=%+v", tools["key"], tools["apps"])
	}
	if !contains(tools["key"].Choices, "home") || !contains(tools["input"].Choices, "hdmi1") {
		t.Error("choices missing")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- a fake brand, so the verbs run end to end without a TV ----

type fakeTV struct {
	asleep bool
	woken  int
	host   string
	token  string
	pinOK  string
	shown  bool
	keys   []device.Key
	input  string
	toasts []string
	slow   time.Duration
	now    device.Now

	noStatus bool // Status fails, as newer firmware does
}

func (f *fakeTV) Info() device.Info {
	return device.Info{ID: "FAKE1", Name: "Fake TV", Brand: "fake", Host: f.host, MAC: "aa:bb:cc:dd:ee:ff"}
}
func (f *fakeTV) Key(ctx context.Context, k device.Key) error {
	if f.slow > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.slow):
		}
	}
	f.keys = append(f.keys, k)
	return nil
}
func (f *fakeTV) Text(context.Context, string) error         { return nil }
func (f *fakeTV) Apps(context.Context) ([]device.App, error) { return nil, nil }
func (f *fakeTV) Launch(context.Context, string) error       { return nil }

// The fake knows one app by name, like a real driver does for the apps
// its TV's list leaves out.
func (f *fakeTV) AppID(name string) (string, bool) {
	if strings.EqualFold(name, "netflix") {
		return "com.netflix.ninja", true
	}
	return "", false
}
func (f *fakeTV) AppName(id string) string {
	if id == "com.netflix.ninja" {
		return "Netflix"
	}
	return ""
}
func (f *fakeTV) Status(context.Context) (map[string]any, error) {
	if f.noStatus {
		return nil, device.Fail("status withheld")
	}
	return map[string]any{"model": "F1"}, nil
}
func (f *fakeTV) Now(context.Context) (device.Now, error) { return f.now, nil }
func (f *fakeTV) Toast(_ context.Context, s string, _ device.ToastOpts) error {
	f.toasts = append(f.toasts, s)
	return nil
}
func (f *fakeTV) Inputs(context.Context) ([]device.Input, error) {
	return []device.Input{{ID: "HDMI_1", Name: "hdmi1", Label: "PS5", Connected: true}, {ID: "HDMI_2", Name: "hdmi2"}}, nil
}
func (f *fakeTV) SwitchInput(_ context.Context, id string) error { f.input = id; return nil }
func (f *fakeTV) Pair(ctx context.Context, ask func(string) (string, error)) (string, error) {
	if err := f.ShowPIN(ctx); err != nil {
		return "", err
	}
	pin, err := ask("PIN")
	if err != nil {
		return "", err
	}
	return f.VerifyPIN(ctx, pin)
}
func (f *fakeTV) ShowPIN(context.Context) error { f.shown = true; return nil }
func (f *fakeTV) VerifyPIN(_ context.Context, pin string) (string, error) {
	if pin != f.pinOK {
		return "", errors.New("wrong PIN")
	}
	f.token = "tok-" + pin
	return f.token, nil
}

type fakeDriver struct{ tv *fakeTV }

func (fakeDriver) Brand() string { return "fake" }
func (fakeDriver) Discover(context.Context, time.Duration) ([]device.Found, error) {
	return nil, nil
}
func (d fakeDriver) Open(cfg device.Config) device.Device {
	d.tv.host = cfg.Host
	d.tv.token = cfg.Token
	return d.tv
}

// useFake registers the fake brand and points config at a temp file holding
// one paired fake TV (unless unpaired), returning the TV for inspection.
func useFake(t *testing.T, paired bool) *fakeTV {
	t.Helper()
	tv := &fakeTV{pinOK: "1234"}
	device.Register(fakeDriver{tv})
	dir := t.TempDir()
	t.Setenv("TVCTL_CONFIG", filepath.Join(dir, "config.json"))
	if paired {
		st, _ := config.Load()
		st.Put("FAKE1", config.Entry{Name: "Fake TV", Brand: "fake", Host: "fake-host", Token: "t0"})
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
	}
	return tv
}

func nobody(string) (string, error) { return "", errors.New("nobody here") }

// runPlain runs one command non-interactively and returns what an
// assistant would read aloud: the plain output, trimmed.
func runPlain(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := Run(args, &Env{Out: &buf, Ask: nobody})
	return strings.TrimSpace(buf.String()), err
}

// runJSON runs one command non-interactively with --json and decodes the
// last JSON document (progress lines may come first).
func runJSON(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	var buf bytes.Buffer
	err := Run(append([]string{"--json"}, args...), &Env{Out: &buf, Ask: nobody})
	// Output is a stream of JSON documents (a progress line, then the
	// result); the last one is the answer.
	dec := json.NewDecoder(&buf)
	var doc map[string]any
	for dec.More() {
		doc = nil
		if jerr := dec.Decode(&doc); jerr != nil {
			t.Fatalf("not JSON: %s (%v)", buf.String(), jerr)
		}
	}
	return doc, err
}

// What an assistant reads aloud: nothing for a key press (the TV shows it),
// one sentence for an outcome, one sentence for an error with no prefix
// and no hint about what to run.
func TestPlainOutputIsSpeakable(t *testing.T) {
	tv := useFake(t, true)
	out, err := runPlain(t, "key", "home", "down")
	if err != nil || out != "" || len(tv.keys) != 2 {
		t.Fatalf("key: %q %v keys=%v", out, err, tv.keys)
	}
	if doc, err := runJSON(t, "key", "home"); err != nil || doc["ok"] != true {
		t.Fatalf("key --json: %v %v", doc, err)
	}
	out, err = runPlain(t, "input", "hdmi2")
	if err != nil || tv.input != "HDMI_2" || out != "Switched to HDMI 2." {
		t.Fatalf("input: %q %v (%q)", out, err, tv.input)
	}
	out, err = runPlain(t, "input", "ps5") // the owner's label, spoken back as the label
	if err != nil || tv.input != "HDMI_1" || out != "Switched to PS5." {
		t.Fatalf("input by label: %q %v (%q)", out, err, tv.input)
	}
	if _, err = runPlain(t, "input", "hdmi9"); err == nil || !strings.Contains(err.Error(), "no input called hdmi9") {
		t.Fatalf("input unknown: %v", err)
	}
	// A capability the TV lacks is refused by capability, not by brand;
	// the fake can't take the companion app, so no "run tv companion".
	if _, err := runPlain(t, "shot"); err == nil || err.Error() != "Fake TV can't do screenshots" {
		t.Fatalf("shot on a TV without screenshots: %v", err)
	}
	out, err = runPlain(t, "toast", "hello")
	if err != nil || len(tv.toasts) != 1 || out != "" {
		t.Fatalf("toast: %q %v", out, err)
	}
	tv.now = device.Now{App: "com.netflix.ninja", State: "playing"}
	out, err = runPlain(t, "now")
	if err != nil || out != "Netflix is playing." {
		t.Fatalf("now: %q %v", out, err)
	}
	out, err = runPlain(t, "pin", "0000")
	if err == nil || out != "No pairing is waiting for a PIN." {
		t.Fatalf("pin error: %q %v", out, err)
	}
	if doc, err := runJSON(t, "pin", "0000"); err == nil || doc["error"] != "no pairing is waiting for a PIN (run tv pair first)" {
		t.Fatalf("pin error --json keeps the full message: %v", doc)
	}
}

// Nobody at the keyboard: pair shows the PIN and stops; pin finishes it,
// storing the token, the id from the TV, and its MAC.
func TestPairThenPin(t *testing.T) {
	tv := useFake(t, false)
	out, err := runPlain(t, "pair", "fake-host", "--brand", "fake")
	if err != nil || !tv.shown || out != "Tell me the PIN on the TV." {
		t.Fatalf("pair: %q %v shown=%v", out, err, tv.shown)
	}
	st, _ := config.Load()
	if st.Pending == nil || st.Pending.Host != "fake-host" || len(st.Devices) != 0 {
		t.Fatalf("pending not saved: %+v", st)
	}
	if out, err = runPlain(t, "pin", "9999"); err == nil || out != "Fake-host didn't accept that PIN." {
		t.Fatalf("wrong PIN: %q %v", out, err)
	}
	doc, err := runJSON(t, "pin", "1 2 3 4") // as a speech model might send it
	if err != nil || doc["id"] != "FAKE1" {
		t.Fatalf("pin: %v %v", doc, err)
	}
	// Re-pairing is idempotent and the plain line is the spoken one.
	st0, _ := config.Load()
	st0.Pending = &config.Pending{Brand: "fake", Name: "fake-host", Host: "fake-host", Started: time.Now()}
	_ = st0.Save()
	if out, err := runPlain(t, "pin", "1234"); err != nil || out != "Connected to fake-host." {
		t.Fatalf("pin plain: %q %v", out, err)
	}
	st, _ = config.Load()
	ent, ok := st.Devices["FAKE1"]
	if !ok || ent.Token != "tok-1234" || ent.MAC != "aa:bb:cc:dd:ee:ff" || st.Pending != nil || st.Default != "FAKE1" {
		t.Fatalf("saved entry: %+v pending=%v default=%q", ent, st.Pending, st.Default)
	}
	// A stale PIN is refused and forgotten.
	st.Pending = &config.Pending{Brand: "fake", Host: "fake-host", Started: time.Now().Add(-time.Hour)}
	_ = st.Save()
	if _, err := runJSON(t, "pin", "1234"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale pin: %v", err)
	}
	// With a person present, pair asks and finishes in one go.
	var buf bytes.Buffer
	err = Run([]string{"pair", "fake-host", "--brand", "fake"},
		&Env{Out: &buf, Interactive: true, Ask: func(string) (string, error) { return "1234", nil }})
	if err != nil || buf.String() != "Read the PIN on the TV.\nConnected to fake-host.\n" {
		t.Fatalf("interactive pair: %v %q", err, buf.String())
	}
}

// A TV that stops answering costs the verb's deadline, then one sentence.
func TestDeadline(t *testing.T) {
	tv := useFake(t, true)
	tv.slow = 2 * time.Second
	if dl := deadlineFor(nil); dl != defaultDeadline {
		t.Fatal("nil command deadline")
	}
	root := Root(&Env{Out: &bytes.Buffer{}})
	key, _, _ := root.Find([]string{"key"})
	if deadlineFor(key) != 5*time.Second {
		t.Fatalf("key deadline %v", deadlineFor(key))
	}
	events, _, _ := root.Find([]string{"events"})
	if deadlineFor(events) != 0 {
		t.Fatal("events should have no deadline")
	}
	// Shrink the key deadline for the test through the annotation.
	key.Annotations[annDeadline] = "200ms"
	root.SetArgs([]string{"--json", "key", "home"})
	t0 := time.Now()
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "didn't answer in time") || time.Since(t0) > time.Second {
		t.Fatalf("deadline: err=%v took %v", err, time.Since(t0))
	}
}

// A verb that changes something wakes a sleeping TV first and still does
// its job; a passive verb leaves it asleep.
func TestAutoWake(t *testing.T) {
	tv := useFake(t, true)
	tv.asleep = true
	if _, err := runPlain(t, "now"); err != nil || tv.woken != 0 {
		t.Fatalf("now should not wake: %v woken=%d", err, tv.woken)
	}
	out, err := runPlain(t, "key", "home")
	if err != nil || out != "" || tv.woken != 1 || len(tv.keys) != 1 {
		t.Fatalf("key on sleeping TV: %q %v woken=%d keys=%v", out, err, tv.woken, tv.keys)
	}
	if _, err := runPlain(t, "vol", "up"); err != nil || tv.woken != 1 {
		t.Fatalf("awake TV woken again: woken=%d", tv.woken)
	}
}

// The schema is what a tool-calling model loads: enums from ValidArgs,
// free text for word lists, parameters in argument order, no companion
// or setup verbs in the voice subset.
func TestSchema(t *testing.T) {
	byName := map[string]map[string]any{}
	for _, tl := range Schema(true) {
		f := tl["function"].(map[string]any)
		byName[f["name"].(string)] = f
	}
	for _, absent := range []string{"tv_pair", "tv_home_add", "tv_events", "tv_tools", "tv_shot"} {
		if _, ok := byName[absent]; ok {
			t.Errorf("%s should not be a voice tool", absent)
		}
	}
	b, _ := json.Marshal(byName["tv_vol"]["parameters"])
	if want := `{"properties":{"vol":{"enum":["up","down","mute"],"type":"string"},"n":{"type":"string"}},"required":["vol"],"type":"object"}`; string(b) != want {
		t.Errorf("vol schema:\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(byName["tv_key"]["parameters"])
	if !strings.Contains(string(b), `"enum":["home"`) || !strings.Contains(string(b), `"required":["key"]`) {
		t.Errorf("key schema: %s", b)
	}
	b, _ = json.Marshal(byName["tv_open"]["parameters"])
	if strings.Contains(string(b), "enum") || !strings.Contains(string(b), `"app or link"`) {
		t.Errorf("open schema: %s", b)
	}
	b, _ = json.Marshal(byName["tv_youtube"]["parameters"])
	if !strings.Contains(string(b), `"words"`) || !strings.Contains(string(b), `"required":[]`) {
		t.Errorf("youtube schema: %s", b)
	}
	if len(Schema(false)) <= len(Schema(true)) {
		t.Error("full schema should be larger than the voice subset")
	}
}

func (f *fakeTV) Asleep(context.Context) bool { return f.asleep }
func (f *fakeTV) Wake(context.Context) error  { f.woken++; f.asleep = false; return nil }

// The merged media verbs: bare play resumes, seek next/prev skip, all via
// media keys when the companion app isn't there.
func TestMergedMediaVerbs(t *testing.T) {
	tv := useFake(t, true)
	for _, c := range [][]string{{"play"}, {"pause"}, {"seek", "next"}, {"seek", "prev"}} {
		if out, err := runPlain(t, c...); err != nil || out != "" {
			t.Fatalf("%v: %q %v", c, out, err)
		}
	}
	want := []device.Key{device.KeyPlayPause, device.KeyPlayPause, device.KeyFastFwd, device.KeyRewind}
	if len(tv.keys) != len(want) {
		t.Fatalf("keys %v, want %v", tv.keys, want)
	}
	for i := range want {
		if tv.keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", tv.keys, want)
		}
	}
	if _, err := runPlain(t, "seek", "+30"); err == nil {
		t.Fatal("time seek without the companion app should fail")
	}
	for _, gone := range []string{"resume", "toggle", "next", "prev", "discover"} {
		if contains(Verbs(), gone) {
			t.Errorf("%q should no longer be a verb", gone)
		}
	}
}

// A TV that could take the companion app but hasn't is told to install
// it; the hint lives in the --json notes, never in the spoken line. The
// real Fire TV driver is used so the Installer capability is the genuine one.
func TestAddonHint(t *testing.T) {
	useFake(t, true)
	st, _ := config.Load()
	st.Put("FIRE1", config.Entry{Name: "Fire TV", Brand: "firetv", Host: "127.0.0.1", Token: "tok"})
	_ = st.Save()
	out, err := runPlain(t, "-d", "Fire TV", "shot")
	if err == nil || out != "Fire TV needs the companion app for screenshots." {
		t.Fatalf("plain: %q %v", out, err)
	}
	doc, _ := runJSON(t, "-d", "Fire TV", "shot")
	if doc["error"] != "Fire TV needs the companion app for screenshots (run tv companion)" {
		t.Fatalf("json: %v", doc)
	}
}

// Every line of --json output is a JSON document, even when pairing has
// something to say on the way (a status call the TV refused).
func TestJSONStaysPure(t *testing.T) {
	tv := useFake(t, false)
	tv.noStatus = true
	var buf bytes.Buffer
	if err := Run([]string{"--json", "pair", "fake-host", "--brand", "fake"}, &Env{Out: &buf, Ask: nobody}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"--json", "pin", "1234"}, &Env{Out: &buf, Ask: nobody}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&buf)
	n := 0
	for dec.More() {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("non-JSON output in --json mode: %v\n%s", err, buf.String())
		}
		n++
	}
	if n < 3 { // pending, waiting-note, connected
		t.Fatalf("expected the warning as its own document, got %d docs", n)
	}
}
