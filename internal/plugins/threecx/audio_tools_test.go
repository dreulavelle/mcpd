package threecx

import (
	"context"
	"strings"
	"testing"
)

// What a caller hears is spread across a queue's own fields, the system's
// music on hold and the prompt files themselves. These tests are mostly about
// the seams between those three: what a queue inherits when it names nothing,
// and what may be concluded when one of the three could not be read.

// A queue as 3CX keeps it, with every audio field set.
const queueWithAudio = `{"Id":2,"Number":"800","Name":"Sales Queue","PollingStrategy":"LongestWaiting",
	"RingTimeout":15,"MasterTimeout":300,"MaxCallersInQueue":10,"SLATime":60,"IsRegistered":true,
	"WrapUpTime":10,"PriorityQueue":false,
	"ForwardNoAnswer":{"To":"Extension","Number":"100","Name":"Alice"},
	"Agents":[{"Id":29,"Number":"100","Name":"Alice","SkillGroup":"1"}],
	"Managers":[{"Id":32,"Number":"101","Name":"Bob"}],
	"OnHoldFile":"onhold.wav","PromptSet":"Acme Prompts","EnableIntro":true,"IntroFile":"queue-intro.wav",
	"PlayFullPrompt":true,"ComfortPrompts":["thanks-for-waiting.wav","still-waiting.wav"],
	"ComfortPromptsEnabled":true,"ComfortPromptsInterval":30,"AnnouncementInterval":20,
	"AnnounceQueuePosition":true,"AnnounceEstimatedWaitTime":true,"AnnounceEstimatedWaitTimeInterval":45,
	"GreetingFile":"greeting.wav","CallbackOfferPrompt":"callback.wav",
	"DestinationNoAnswerPrompt":"sorry.wav"}`

// The same queue with no audio of its own, which is the ordinary case: it
// plays whatever the system plays.
const queueWithoutAudio = `{"Id":3,"Number":"801","Name":"Support Queue","PollingStrategy":"Hunt",
	"RingTimeout":20,"MasterTimeout":600,"MaxCallersInQueue":25,"IsRegistered":true,
	"ForwardNoAnswer":{"To":"VoiceMail","Number":"100"},"Agents":[],"Managers":[]}`

func audioFixtures() map[string]string {
	fx := acmeFixtures()
	fx["Queues"] = collection(1, queueWithAudio)
	fx["MusicOnHoldSettings"] = `{"Id":1,"MusicOnHold":"system-hold.wav","MusicOnHold1":"jazz.wav",
		"MusicOnHoldRandomize":true,"MusicOnHoldRandomizePerCall":false}`
	fx["CustomPrompts"] = collection(3,
		`{"Filename":"onhold.wav","DisplayName":"On hold","PromptType":"File","CanBeDeleted":true,"FileLink":"https://pbx.example/prompts/onhold.wav"}`,
		`{"Filename":"system-hold.wav","DisplayName":"System hold","PromptType":"File","CanBeDeleted":false}`,
		`{"Filename":"jazz.wav","DisplayName":"Jazz","PromptType":"Playlist","CanBeDeleted":true}`)
	fx["Receptionists"] = collection(1,
		`{"Id":4,"Number":"900","Name":"Main menu","PromptFilename":"welcome.wav","PromptSet":"Acme Prompts"}`)
	fx["RingGroups"] = collection(1,
		`{"Id":5,"Number":"802","Name":"Front desk","GreetingFile":"onhold.wav"}`)
	return fx
}

// One queue read in full: the numbers a technician needs and everything a
// caller hears, with the intervals that decide how often they hear it.
func TestGetQueue_ReadsEverythingACallerHears(t *testing.T) {
	p, _ := toolPlugin(t, audioFixtures())
	q, err := p.getQueue(context.Background(), queueArgs{Queue: "800"})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, q, credentialWords...)

	if q.Number != "800" || q.Name != "Sales Queue" || q.Customer != "Acme" {
		t.Errorf("queue: %+v", q)
	}
	if q.MusicOnHold.File != "onhold.wav" || q.MusicOnHold.Inherited {
		t.Errorf("a queue with its own music is not inheriting: %+v", q.MusicOnHold)
	}
	if q.MusicOnHold.Playlist != "Acme Prompts" {
		t.Errorf("the prompt set should come through: %+v", q.MusicOnHold)
	}
	if !q.IntroPrompt.Enabled || q.IntroPrompt.File != "queue-intro.wav" {
		t.Errorf("intro: %+v", q.IntroPrompt)
	}
	if !q.ComfortPrompt.Enabled || q.ComfortPrompt.File != "thanks-for-waiting.wav" ||
		q.ComfortPrompt.IntervalSeconds != 30 {
		t.Errorf("comfort: %+v", q.ComfortPrompt)
	}
	// 3CX keeps a list, and reporting only the first would hide the rest.
	if len(q.ComfortPrompt.Files) != 2 {
		t.Errorf("every comfort prompt should be reported: %+v", q.ComfortPrompt.Files)
	}
	if q.Announcements.IntervalSeconds != 20 || !q.Announcements.QueuePosition ||
		q.Announcements.EstimatedWaitIntervalSec != 45 {
		t.Errorf("announcements: %+v", q.Announcements)
	}
	if q.GreetingFile != "greeting.wav" || q.CallbackOfferPrompt != "callback.wav" ||
		q.NoAnswerPrompt != "sorry.wav" {
		t.Errorf("the rest of the audio: %+v", q)
	}
	if q.Agents[0] != "100 (Alice) skill 1" || q.NoAnswer != "Extension 100 (Alice)" {
		t.Errorf("the queue's own configuration should still read as it did: %+v", q)
	}
}

// A queue that names no music of its own plays the system's, and saying only
// "none" would have somebody change a queue setting that was never the cause.
func TestGetQueue_SaysWhatItInheritsWhenItNamesNoMusic(t *testing.T) {
	fx := audioFixtures()
	fx["Queues"] = collection(1, queueWithoutAudio)
	p, _ := toolPlugin(t, fx)

	q, err := p.getQueue(context.Background(), queueArgs{Queue: "801"})
	if err != nil {
		t.Fatal(err)
	}
	if !q.MusicOnHold.Inherited {
		t.Errorf("a queue with no music of its own inherits: %+v", q.MusicOnHold)
	}
	if q.MusicOnHold.File != "system-hold.wav" {
		t.Errorf("what it inherits should be named: %+v", q.MusicOnHold)
	}
	if !strings.Contains(q.MusicOnHold.Source, "system") {
		t.Errorf("the source should say where the audio comes from: %+v", q.MusicOnHold)
	}
}

// A build that does not serve the system's music leaves the question open
// rather than answering it wrongly: an empty file here would read as a queue
// that plays silence.
func TestGetQueue_LeavesInheritanceOpenWhenTheSystemWillNotSay(t *testing.T) {
	fx := audioFixtures()
	fx["Queues"] = collection(1, queueWithoutAudio)
	delete(fx, "MusicOnHoldSettings")
	p, _ := toolPlugin(t, fx, "MusicOnHoldSettings")

	q, err := p.getQueue(context.Background(), queueArgs{Queue: "801"})
	if err != nil {
		t.Fatalf("a missing endpoint should not fail the whole read: %v", err)
	}
	if q.MusicOnHold.File != "" || !q.MusicOnHold.Inherited {
		t.Errorf("nothing is known about what it falls back to: %+v", q.MusicOnHold)
	}
	if len(q.Unavailable) == 0 || !strings.Contains(strings.Join(q.Unavailable, " "), "music on hold") {
		t.Errorf("the answer should say which part is missing: %+v", q.Unavailable)
	}
}

func TestGetQueue_UnknownQueueSaysWhereToLook(t *testing.T) {
	fx := audioFixtures()
	fx["Queues"] = collection(0)
	p, _ := toolPlugin(t, fx)

	_, err := p.getQueue(context.Background(), queueArgs{Queue: "999"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "list_queues") {
		t.Errorf("the refusal should name the tool that finds the number, got %v", err)
	}
}

// The file list, the system's slots, and which of the files sit in them.
func TestListMusicOnHold_NamesTheSlotsAndWhatIsInThem(t *testing.T) {
	p, _ := toolPlugin(t, audioFixtures())
	res, err := p.listMusicOnHold(context.Background(), musicOnHoldArgs{})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, res, credentialWords...)
	// FileLink is the URL the file is downloaded from. The transport refuses
	// it by name, and it is never asked for, so it must not appear here.
	mustNotContain(t, res, "FileLink", "https://pbx.example/prompts")

	if res.SystemWide == nil || len(res.SystemWide.Slots) != 2 || !res.SystemWide.Randomize {
		t.Fatalf("system music on hold: %+v", res.SystemWide)
	}
	if res.SystemWide.Slots[0].File != "system-hold.wav" || res.SystemWide.Slots[0].Slot != 0 {
		t.Errorf("the first slot: %+v", res.SystemWide.Slots[0])
	}
	if res.Returned != 3 {
		t.Fatalf("every file should be listed: %+v", res.Files)
	}

	byName := map[string]AudioFileRow{}
	for _, f := range res.Files {
		byName[f.Filename] = f
	}
	if f := byName["system-hold.wav"]; !f.InUse || f.SystemSlot != 0 {
		t.Errorf("a file in a slot should say so: %+v", f)
	}
	if f := byName["onhold.wav"]; f.InUse || f.SystemSlot != -1 {
		t.Errorf("a file in no slot is not in use by the system: %+v", f)
	}
	if f := byName["jazz.wav"]; f.Type != "Playlist" {
		t.Errorf("the phone system's own type should come through: %+v", f)
	}
	if f := byName["system-hold.wav"]; f.Removable {
		t.Errorf("a stock prompt cannot be deleted: %+v", f)
	}
}

// A build without the file list still answers for the system's slots, and says
// which half is missing rather than reading as a system with no audio on it.
func TestListMusicOnHold_SaysWhatThisBuildWillNotAnswerFor(t *testing.T) {
	fx := audioFixtures()
	delete(fx, "CustomPrompts")
	p, _ := toolPlugin(t, fx, "CustomPrompts")

	res, err := p.listMusicOnHold(context.Background(), musicOnHoldArgs{})
	if err != nil {
		t.Fatalf("one missing endpoint should not fail the tool: %v", err)
	}
	if res.SystemWide == nil {
		t.Error("the system's slots were readable and should still be reported")
	}
	if len(res.Unavailable) == 0 {
		t.Error("the answer should say the file list could not be read")
	}
}

// Every place one file is named, across the four kinds of object that can name
// one.
func TestSearchAudioUsage_FindsEveryPlaceAFileIsNamed(t *testing.T) {
	p, _ := toolPlugin(t, audioFixtures())
	res, err := p.searchAudioUsage(context.Background(), audioUsageArgs{Filename: "onhold.wav"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exists == nil || !*res.Exists {
		t.Errorf("the file is in the file list: %+v", res.Exists)
	}
	kinds := map[string]string{}
	for _, u := range res.Usages {
		kinds[u.Kind] = u.Setting
	}
	if kinds["queue"] != "music on hold" {
		t.Errorf("the queue's music on hold should be found: %+v", res.Usages)
	}
	if _, ok := kinds["ring group"]; !ok {
		t.Errorf("a ring group greeting names audio too: %+v", res.Usages)
	}
	if len(res.Unavailable) != 0 {
		t.Errorf("everything was readable: %+v", res.Unavailable)
	}
	if !strings.Contains(res.Summary, "used in") {
		t.Errorf("summary: %q", res.Summary)
	}
}

// The three findings this tool has to keep apart. Read from the fields alone
// they look alike, and "nothing uses it" is the sentence somebody deletes a
// file on.
func TestSearchAudioUsage_TellsMissingFromUnusedFromUnknown(t *testing.T) {
	t.Run("no such file", func(t *testing.T) {
		p, _ := toolPlugin(t, audioFixtures())
		res, err := p.searchAudioUsage(context.Background(), audioUsageArgs{Filename: "nowhere.wav"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Exists == nil || *res.Exists {
			t.Errorf("the phone system has no such file: %+v", res.Exists)
		}
		if !strings.Contains(res.Summary, "no audio file called") {
			t.Errorf("summary: %q", res.Summary)
		}
	})

	t.Run("exists but nothing uses it", func(t *testing.T) {
		fx := audioFixtures()
		fx["Queues"] = collection(1, queueWithoutAudio)
		fx["RingGroups"] = collection(0)
		fx["MusicOnHoldSettings"] = `{"Id":1}`
		p, _ := toolPlugin(t, fx)

		res, err := p.searchAudioUsage(context.Background(), audioUsageArgs{Filename: "onhold.wav"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Usages) != 0 {
			t.Fatalf("nothing should be using it: %+v", res.Usages)
		}
		if !strings.Contains(res.Summary, "nothing in") || !strings.Contains(res.Summary, "uses it") {
			t.Errorf("summary: %q", res.Summary)
		}
	})

	// The one worth being loud about: a search that could not cover everything
	// must not read as proof the file is unused.
	t.Run("could not be fully asked", func(t *testing.T) {
		fx := audioFixtures()
		fx["Queues"] = collection(1, queueWithoutAudio)
		fx["RingGroups"] = collection(0)
		fx["MusicOnHoldSettings"] = `{"Id":1}`
		delete(fx, "Receptionists")
		p, _ := toolPlugin(t, fx, "Receptionists")

		res, err := p.searchAudioUsage(context.Background(), audioUsageArgs{Filename: "onhold.wav"})
		if err != nil {
			t.Fatalf("a missing endpoint should not fail the tool: %v", err)
		}
		if len(res.Unavailable) == 0 {
			t.Fatal("the answer should say what could not be searched")
		}
		if !strings.Contains(res.Summary, "not a complete answer") ||
			!strings.Contains(res.Summary, "Do not treat") {
			t.Errorf("the summary must not let this read as unused: %q", res.Summary)
		}
	})
}

// The listing carries the short form, and a queue with no audio of its own
// carries none at all rather than a row of blanks.
func TestListQueues_CarriesAnAudioSummary(t *testing.T) {
	fx := audioFixtures()
	fx["Queues"] = collection(2, queueWithAudio, queueWithoutAudio)
	p, _ := toolPlugin(t, fx)

	res, err := p.listQueues(context.Background(), queuesArgs{})
	if err != nil || res.Returned != 2 {
		t.Fatalf("queues: %+v %v", res, err)
	}
	withAudio, without := res.Queues[0], res.Queues[1]
	if withAudio.Audio == nil || withAudio.Audio.MusicOnHold != "onhold.wav" ||
		withAudio.Audio.IntroPrompt != "queue-intro.wav" ||
		withAudio.Audio.Comfort != "thanks-for-waiting.wav" {
		t.Errorf("the summary should name what the queue plays: %+v", withAudio.Audio)
	}
	if without.Audio != nil {
		t.Errorf("a queue naming no audio carries no summary: %+v", without.Audio)
	}
	// The existing fields must read exactly as they did before the summary
	// was added to this row.
	if withAudio.MaxWaitSeconds != 300 || withAudio.NoAnswer != "Extension 100 (Alice)" {
		t.Errorf("queue: %+v", withAudio)
	}
}

// Two names for the same file: 3CX stores a prompt sometimes bare and
// sometimes with the folder it sits in, and the console shows the bare name.
func TestAudioFilenamesCompareByTheirBareName(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"onhold.wav", "onhold.wav", true},
		{"OnHold.WAV", "onhold.wav", true},
		{`prompts\onhold.wav`, "onhold.wav", true},
		{"/var/lib/3cxpbx/Data/Http/prompts/onhold.wav", "onhold.wav", true},
		{"onhold.wav", "other.wav", false},
		{"", "onhold.wav", false},
		{"", "", false},
	} {
		if got := sameAudioFile(tc.a, tc.b); got != tc.want {
			t.Errorf("sameAudioFile(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
