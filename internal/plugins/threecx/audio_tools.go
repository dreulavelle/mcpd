package threecx

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "what does a caller actually hear".
//
// The complaint these settle is the one a queue listing never will: a customer
// rings, waits, and hears the wrong music, somebody else's greeting, or
// silence. The answer is spread across a queue's own fields, the system's
// music on hold, and the prompt files themselves -- three different places in
// the API, none of which names the others.
//
// Every read here names its fields, for the reason the extension tools give.
// Nothing on these entities is a credential, but CustomPrompt carries
// FileLink, a URL the file is downloaded from, and the transport refuses it by
// name -- so it is never asked for.

func (p *Plugin) registerAudioTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_queue",
		Title: "Get one call queue",
		Description: "One queue in full: strategy, ring and wait times, agents and " +
			"managers, and everything a caller hears -- music on hold, the intro " +
			"prompt, comfort prompts and their interval, and the announcements.",
		Idempotent: true,
	}, p.getQueue)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_music_on_hold",
		Title: "List music on hold and prompt files",
		Description: "The audio files on the phone system, the system's music on hold " +
			"and which slot each file sits in, and the prompt sets a queue or a " +
			"digital receptionist can name.",
		Idempotent: true,
	}, p.listMusicOnHold)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_audio_usage",
		Title: "Search for what uses an audio file",
		Description: "Everything that names one audio file: queues, digital " +
			"receptionists, ring groups and the system's music on hold. Says " +
			"whether the file exists at all, and whether nothing is using it.",
		Idempotent: true,
	}, p.searchAudioUsage)
}

// --- what a queue is asked for ------------------------------------------------

// queueAudioFields are the queue properties that decide what a caller hears.
// Named separately from the rest so the same list serves the detailed read and
// the summary on a listing, and the two can never drift apart.
const queueAudioFields = "OnHoldFile,PromptSet,EnableIntro,IntroFile,PlayFullPrompt," +
	"ComfortPrompts,ComfortPromptsEnabled,ComfortPromptsInterval," +
	"AnnouncementInterval,AnnounceQueuePosition,AnnounceEstimatedWaitTime," +
	"AnnounceEstimatedWaitTimeInterval,GreetingFile,CallbackOfferPrompt," +
	"DestinationNoAnswerPrompt"

// queueFields is everything one queue is read with.
const queueFields = "Id,Number,Name,PollingStrategy,RingTimeout,MasterTimeout," +
	"MaxCallersInQueue,SLATime,IsRegistered,WrapUpTime,PriorityQueue,ForwardNoAnswer," +
	queueAudioFields

const queueExpand = "Agents($select=Id,Number,Name,SkillGroup),Managers($select=Id,Number,Name)"

// queueAudioRecord is the audio half of a queue as 3CX keeps it.
//
// Embedded rather than repeated, so the listing and the detailed read decode
// the same names from the same projection.
type queueAudioRecord struct {
	OnHoldFile             string   `json:"OnHoldFile"`
	PromptSet              string   `json:"PromptSet"`
	EnableIntro            bool     `json:"EnableIntro"`
	IntroFile              string   `json:"IntroFile"`
	PlayFullPrompt         bool     `json:"PlayFullPrompt"`
	ComfortPrompts         []string `json:"ComfortPrompts"`
	ComfortPromptsEnabled  bool     `json:"ComfortPromptsEnabled"`
	ComfortPromptsInterval int      `json:"ComfortPromptsInterval"`
	AnnouncementInterval   int      `json:"AnnouncementInterval"`
	AnnounceQueuePosition  bool     `json:"AnnounceQueuePosition"`
	AnnounceWaitTime       bool     `json:"AnnounceEstimatedWaitTime"`
	AnnounceWaitInterval   int      `json:"AnnounceEstimatedWaitTimeInterval"`
	GreetingFile           string   `json:"GreetingFile"`
	CallbackOfferPrompt    string   `json:"CallbackOfferPrompt"`
	NoAnswerPrompt         string   `json:"DestinationNoAnswerPrompt"`
}

type queueRecord struct {
	Number          string       `json:"Number"`
	Name            string       `json:"Name"`
	PollingStrategy string       `json:"PollingStrategy"`
	RingTimeout     int          `json:"RingTimeout"`
	MasterTimeout   int          `json:"MasterTimeout"`
	MaxCallers      int          `json:"MaxCallersInQueue"`
	SLATime         int          `json:"SLATime"`
	IsRegistered    bool         `json:"IsRegistered"`
	WrapUpTime      int          `json:"WrapUpTime"`
	PriorityQueue   bool         `json:"PriorityQueue"`
	NoAnswer        *destination `json:"ForwardNoAnswer"`
	Agents          []member     `json:"Agents"`
	Managers        []member     `json:"Managers"`
	queueAudioRecord
}

// --- the normalised audio a queue plays ---------------------------------------

// QueueMusicOnHold is what a caller hears while they wait.
type QueueMusicOnHold struct {
	// File is the queue's own music on hold. Empty means the queue plays
	// whatever the system plays.
	File string `json:"file,omitempty"`
	// Source says where the audio comes from, in the words the 3CX console
	// uses: the queue's own, or the system's.
	Source string `json:"source"`
	// Playlist is the prompt set the queue names, where it names one.
	Playlist string `json:"playlist,omitempty"`
	// Inherited reports that the queue sets none of its own, so changing the
	// system's music on hold changes what this queue plays.
	Inherited bool `json:"inherited"`
}

// QueuePrompt is one prompt a queue plays and whether it is turned on.
type QueuePrompt struct {
	Enabled bool   `json:"enabled"`
	File    string `json:"file,omitempty"`
}

// QueueComfortPrompt is the prompt repeated to a caller who is still waiting.
type QueueComfortPrompt struct {
	Enabled bool   `json:"enabled"`
	File    string `json:"file,omitempty"`
	// Files is every comfort prompt when there is more than one, because 3CX
	// keeps a list and reporting only the first would hide the rest.
	Files           []string `json:"files,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
}

// QueueAnnouncements is what the queue tells a caller while they wait.
type QueueAnnouncements struct {
	IntervalSeconds          int  `json:"interval_seconds,omitempty"`
	QueuePosition            bool `json:"queue_position"`
	EstimatedWaitTime        bool `json:"estimated_wait_time"`
	EstimatedWaitIntervalSec int  `json:"estimated_wait_interval_seconds,omitempty"`
}

// Queue is one call queue in full.
type Queue struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string `json:"customer"`
	Number   string `json:"number"`
	Name     string `json:"name"`

	Strategy       string `json:"strategy,omitempty"`
	RingSeconds    int    `json:"ring_seconds"`
	MaxWaitSeconds int    `json:"max_wait_seconds"`
	MaxCallers     int    `json:"max_callers"`
	SLASeconds     int    `json:"sla_seconds,omitempty"`
	WrapUpSeconds  int    `json:"wrap_up_seconds,omitempty"`
	Priority       bool   `json:"priority"`
	Registered     bool   `json:"registered"`

	Agents   []string `json:"agents"`
	Managers []string `json:"managers"`
	NoAnswer string   `json:"no_answer"`

	MusicOnHold   QueueMusicOnHold   `json:"music_on_hold"`
	IntroPrompt   QueuePrompt        `json:"intro_prompt"`
	ComfortPrompt QueueComfortPrompt `json:"comfort_prompt"`
	Announcements QueueAnnouncements `json:"announcements"`

	// GreetingFile, CallbackOfferPrompt and NoAnswerPrompt are the rest of the
	// audio a queue can name. Present only where the queue sets them.
	GreetingFile        string `json:"greeting_file,omitempty"`
	CallbackOfferPrompt string `json:"callback_offer_prompt,omitempty"`
	NoAnswerPrompt      string `json:"no_answer_prompt,omitempty"`
	// PlaysFullPrompt reports that a prompt finishes before an agent is
	// connected, which is the usual cause of "the caller heard the whole
	// message before I picked up".
	PlaysFullPrompt bool `json:"plays_full_prompt"`

	// Unavailable names anything this build of 3CX would not answer for, so an
	// incomplete answer says which part is missing rather than reading as a
	// queue with nothing configured.
	Unavailable []string `json:"unavailable,omitempty"`
}

type queueArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Queue    string `json:"queue" jsonschema:"the queue's number, as list_queues reports it; its exact name also works"`
}

func (p *Plugin) getQueue(ctx context.Context, args queueArgs) (Queue, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return Queue{}, err
	}
	asked := strings.TrimSpace(args.Queue)
	if asked == "" {
		return Queue{}, fmt.Errorf("queue is required: the queue's number, as list_queues reports it")
	}

	// By number first, because that is what a queue is identified by and it
	// cannot match two. A name is tried only when no number matched, so a
	// queue named after another's number can never be returned in its place.
	rec, found, err := p.readQueue(ctx, acct, "Number eq "+odataString(asked))
	if err != nil {
		return Queue{}, acct.call(err)
	}
	if !found {
		rec, found, err = p.readQueue(ctx, acct, "Name eq "+odataString(asked))
		if err != nil {
			return Queue{}, acct.call(err)
		}
	}
	if !found {
		return Queue{}, fmt.Errorf("there is no queue %q on this phone system; "+
			"list_queues has every queue with its number", asked)
	}

	out := Queue{
		Number: rec.Number, Name: rec.Name, Strategy: rec.PollingStrategy,
		RingSeconds: rec.RingTimeout, MaxWaitSeconds: rec.MasterTimeout,
		MaxCallers: rec.MaxCallers, SLASeconds: rec.SLATime, WrapUpSeconds: rec.WrapUpTime,
		Priority: rec.PriorityQueue, Registered: rec.IsRegistered,
		Agents: memberTexts(rec.Agents), Managers: memberTexts(rec.Managers),
		NoAnswer:            rec.NoAnswer.text(),
		GreetingFile:        rec.GreetingFile,
		CallbackOfferPrompt: rec.CallbackOfferPrompt,
		NoAnswerPrompt:      rec.NoAnswerPrompt,
		PlaysFullPrompt:     rec.PlayFullPrompt,
		MusicOnHold:         musicOnHoldOf(rec.queueAudioRecord),
		IntroPrompt:         QueuePrompt{Enabled: rec.EnableIntro, File: rec.IntroFile},
		ComfortPrompt:       comfortOf(rec.queueAudioRecord),
		Announcements: QueueAnnouncements{
			IntervalSeconds:          rec.AnnouncementInterval,
			QueuePosition:            rec.AnnounceQueuePosition,
			EstimatedWaitTime:        rec.AnnounceWaitTime,
			EstimatedWaitIntervalSec: rec.AnnounceWaitInterval,
		},
	}

	// What the queue inherits is only knowable from the system's own setting,
	// and a build that does not serve it leaves the question open rather than
	// answered wrongly.
	if out.MusicOnHold.Inherited {
		sys, err := p.readSystemMusicOnHold(ctx, acct)
		switch {
		case err != nil && missing(err):
			out.Unavailable = append(out.Unavailable,
				"the system's music on hold, so what this queue falls back to is not known")
		case err != nil:
			return Queue{}, acct.call(err)
		default:
			out.MusicOnHold.File = sys.first()
		}
	}

	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// readQueue reads one queue by an OData filter.
func (p *Plugin) readQueue(ctx context.Context, acct *account, filter string) (queueRecord, bool, error) {
	q := url.Values{
		"$select": {queueFields},
		"$expand": {queueExpand},
		"$filter": {filter},
	}
	return one[queueRecord](ctx, acct.client, "Queues", q)
}

// musicOnHoldOf works out what a queue plays and where it comes from.
func musicOnHoldOf(a queueAudioRecord) QueueMusicOnHold {
	moh := QueueMusicOnHold{File: a.OnHoldFile, Playlist: a.PromptSet}
	if strings.TrimSpace(a.OnHoldFile) == "" {
		moh.Inherited = true
		moh.Source = "the system's music on hold"
		return moh
	}
	moh.Source = "this queue"
	return moh
}

func comfortOf(a queueAudioRecord) QueueComfortPrompt {
	out := QueueComfortPrompt{
		Enabled:         a.ComfortPromptsEnabled,
		IntervalSeconds: a.ComfortPromptsInterval,
	}
	files := make([]string, 0, len(a.ComfortPrompts))
	for _, f := range a.ComfortPrompts {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	if len(files) > 0 {
		out.File = files[0]
	}
	if len(files) > 1 {
		out.Files = files
	}
	return out
}

// --- the system's music on hold -----------------------------------------------

// mohSettingsFields names the ten slots the console shows and the two
// behaviours. The property names are 3CX's: one bare and nine numbered.
const mohSettingsFields = "Id,MusicOnHold,MusicOnHold1,MusicOnHold2,MusicOnHold3," +
	"MusicOnHold4,MusicOnHold5,MusicOnHold6,MusicOnHold7,MusicOnHold8,MusicOnHold9," +
	"MusicOnHoldRandomize,MusicOnHoldRandomizePerCall"

type mohSettings struct {
	MusicOnHold  string `json:"MusicOnHold"`
	MusicOnHold1 string `json:"MusicOnHold1"`
	MusicOnHold2 string `json:"MusicOnHold2"`
	MusicOnHold3 string `json:"MusicOnHold3"`
	MusicOnHold4 string `json:"MusicOnHold4"`
	MusicOnHold5 string `json:"MusicOnHold5"`
	MusicOnHold6 string `json:"MusicOnHold6"`
	MusicOnHold7 string `json:"MusicOnHold7"`
	MusicOnHold8 string `json:"MusicOnHold8"`
	MusicOnHold9 string `json:"MusicOnHold9"`
	Randomize    bool   `json:"MusicOnHoldRandomize"`
	PerCall      bool   `json:"MusicOnHoldRandomizePerCall"`
}

// slots returns the ten slots in the order the console shows them, with the
// slot number each one sits in. Empty slots are left out.
func (m mohSettings) slots() []SystemMusicSlot {
	all := []string{m.MusicOnHold, m.MusicOnHold1, m.MusicOnHold2, m.MusicOnHold3,
		m.MusicOnHold4, m.MusicOnHold5, m.MusicOnHold6, m.MusicOnHold7,
		m.MusicOnHold8, m.MusicOnHold9}
	out := make([]SystemMusicSlot, 0, len(all))
	for i, f := range all {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, SystemMusicSlot{Slot: i, File: f})
		}
	}
	return out
}

// first is what a queue with no music of its own falls back to.
func (m mohSettings) first() string {
	if s := m.slots(); len(s) > 0 {
		return s[0].File
	}
	return ""
}

func (p *Plugin) readSystemMusicOnHold(ctx context.Context, acct *account) (mohSettings, error) {
	var m mohSettings
	q := url.Values{"$select": {mohSettingsFields}}
	err := acct.client.get(ctx, "MusicOnHoldSettings", q, &m)
	return m, err
}

// --- list_music_on_hold -------------------------------------------------------

// SystemMusicSlot is one of the system's music-on-hold slots.
type SystemMusicSlot struct {
	// Slot is the position in the console's list, from zero.
	Slot int    `json:"slot"`
	File string `json:"file"`
}

// SystemMusicOnHold is what the phone system plays when nothing overrides it.
type SystemMusicOnHold struct {
	Slots []SystemMusicSlot `json:"slots"`
	// Randomize and RandomizePerCall are how 3CX picks between the slots.
	Randomize        bool `json:"randomize"`
	RandomizePerCall bool `json:"randomize_per_call"`
}

// AudioFileRow is one audio file on the phone system.
type AudioFileRow struct {
	// Index is this file's position in the listing. The API gives a prompt file
	// no id of its own, so there is nothing more stable to quote; the filename
	// is what every other object refers to it by.
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	DisplayName string `json:"display_name,omitempty"`
	// Type is the phone system's own: File, DepFile or Playlist.
	Type string `json:"type,omitempty"`
	// SystemSlot is the music-on-hold slot this file sits in, or -1 when it is
	// not one of them. This, rather than an "enabled" flag: the API keeps no
	// per-file enabled state, and inventing one would be a field nobody could
	// act on.
	SystemSlot int `json:"system_slot"`
	// InUse reports that the system's music on hold names this file. It says
	// nothing about queues, which find_audio_usage answers properly.
	InUse bool `json:"in_use_by_system"`
	// Removable is 3CX's own CanBeDeleted: a stock prompt cannot be removed.
	Removable bool `json:"removable"`
}

// PromptSetRow is one prompt set: a named collection of prompts a queue or a
// digital receptionist can point at.
type PromptSetRow struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Language string `json:"language,omitempty"`
	Folder   string `json:"folder,omitempty"`
	Version  string `json:"version,omitempty"`
	// Files are the prompts in the set, by filename.
	Files    []string `json:"files,omitempty"`
	Returned int      `json:"returned"`
}

// MusicOnHoldResult is every source of audio on one phone system.
type MusicOnHoldResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string             `json:"customer"`
	System   *SystemMusicOnHold `json:"system,omitempty"`
	Files    []AudioFileRow     `json:"files"`
	Sets     []PromptSetRow     `json:"prompt_sets,omitempty"`
	Returned int                `json:"returned"`
	// Unavailable names what this build of 3CX would not answer for, so a short
	// answer says which part is missing rather than reading as a phone system
	// with no audio on it.
	Unavailable []string `json:"unavailable,omitempty"`
	truncation
}

type musicOnHoldArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query    string `json:"query,omitempty" jsonschema:"only files whose filename or display name contains this"`
	// IncludeSets is off by default: a system prompt set holds a few hundred
	// files, which is a great deal of answer for a question about music.
	IncludeSets bool `json:"include_prompt_sets,omitempty" jsonschema:"also list the prompt sets and what is in them"`
	Limit       int  `json:"limit,omitempty" jsonschema:"most files to return; the instance's ceiling applies"`
}

func (p *Plugin) listMusicOnHold(ctx context.Context, args musicOnHoldArgs) (MusicOnHoldResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return MusicOnHoldResult{}, err
	}
	out := MusicOnHoldResult{Files: []AudioFileRow{}}

	// The system's slots first: they decide which of the files below are in
	// use, so the listing can say so on each row.
	inSlot := map[string]int{}
	sys, err := p.readSystemMusicOnHold(ctx, acct)
	switch {
	case err != nil && missing(err):
		out.Unavailable = append(out.Unavailable, "the system's music on hold")
	case err != nil:
		return MusicOnHoldResult{}, acct.call(err)
	default:
		slots := sys.slots()
		out.System = &SystemMusicOnHold{
			Slots: slots, Randomize: sys.Randomize, RandomizePerCall: sys.PerCall,
		}
		for _, s := range slots {
			if _, seen := inSlot[audioKey(s.File)]; !seen {
				inSlot[audioKey(s.File)] = s.Slot
			}
		}
	}

	// FileLink is deliberately not asked for: it is the URL the file is
	// downloaded from, and the transport refuses it by name.
	q := url.Values{
		"$select":  {"Filename,DisplayName,PromptType,CanBeDeleted"},
		"$orderby": {"Filename"},
	}
	got, err := list[struct {
		Filename     string `json:"Filename"`
		DisplayName  string `json:"DisplayName"`
		PromptType   string `json:"PromptType"`
		CanBeDeleted bool   `json:"CanBeDeleted"`
	}](ctx, acct.client, "CustomPrompts", q, p.limitOf(args.Limit))
	switch {
	case err != nil && missing(err):
		out.Unavailable = append(out.Unavailable, "the list of audio files")
	case err != nil:
		return MusicOnHoldResult{}, acct.call(err)
	default:
		for i, f := range got.Rows {
			if !matches(args.Query, f.Filename, f.DisplayName) {
				continue
			}
			slot, used := inSlot[audioKey(f.Filename)]
			if !used {
				slot = -1
			}
			out.Files = append(out.Files, AudioFileRow{
				Index: i, Filename: f.Filename, DisplayName: f.DisplayName,
				Type: f.PromptType, SystemSlot: slot, InUse: used, Removable: f.CanBeDeleted,
			})
		}
		out.Files, out.truncation = bound(out.Files, got.reason())
	}
	out.Returned = len(out.Files)

	if args.IncludeSets {
		sets, err := p.readPromptSets(ctx, acct)
		switch {
		case err != nil && missing(err):
			out.Unavailable = append(out.Unavailable, "the prompt sets")
		case err != nil:
			return MusicOnHoldResult{}, acct.call(err)
		default:
			out.Sets = sets
		}
	}

	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

func (p *Plugin) readPromptSets(ctx context.Context, acct *account) ([]PromptSetRow, error) {
	q := url.Values{
		"$select":  {"Id,PromptSetName,PromptSetType,LanguageCode,Folder,Version"},
		"$expand":  {"Prompts($select=Id,Filename)"},
		"$orderby": {"PromptSetName"},
	}
	got, err := list[struct {
		ID       int    `json:"Id"`
		Name     string `json:"PromptSetName"`
		Type     string `json:"PromptSetType"`
		Language string `json:"LanguageCode"`
		Folder   string `json:"Folder"`
		Version  string `json:"Version"`
		Prompts  []struct {
			Filename string `json:"Filename"`
		} `json:"Prompts"`
	}](ctx, acct.client, "PromptSets", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	out := make([]PromptSetRow, 0, len(got.Rows))
	for _, s := range got.Rows {
		row := PromptSetRow{
			ID: s.ID, Name: s.Name, Type: s.Type, Language: s.Language,
			Folder: s.Folder, Version: s.Version, Returned: len(s.Prompts),
		}
		// The filenames, bounded: a system prompt set holds hundreds, and the
		// question this answers is which set to name, not what is in all of them.
		for i, pr := range s.Prompts {
			if i == promptsNamedPerSet {
				break
			}
			row.Files = append(row.Files, pr.Filename)
		}
		out = append(out, row)
	}
	return out, nil
}

// promptsNamedPerSet bounds how many of a set's files are spelt out. A system
// set holds several hundred, and naming them all costs more context than the
// answer is worth.
const promptsNamedPerSet = 20

// --- the summary a listing carries --------------------------------------------

// QueueAudioSummary is the short answer to "what does this queue play",
// carried on every row of list_queues so the odd one out can be spotted
// without opening each queue in turn.
//
// Nil when the queue names no audio of its own, rather than an object of empty
// strings: a queue that plays the system's music and nothing else has nothing
// to say here, and a row of blanks reads as a queue whose audio failed to load.
type QueueAudioSummary struct {
	MusicOnHold string `json:"music_on_hold,omitempty"`
	IntroPrompt string `json:"intro_prompt,omitempty"`
	Comfort     string `json:"comfort_prompt,omitempty"`
	PromptSet   string `json:"prompt_set,omitempty"`
}

func audioSummaryOf(a queueAudioRecord) *QueueAudioSummary {
	out := QueueAudioSummary{
		MusicOnHold: strings.TrimSpace(a.OnHoldFile),
		IntroPrompt: strings.TrimSpace(a.IntroFile),
		PromptSet:   strings.TrimSpace(a.PromptSet),
	}
	for _, c := range a.ComfortPrompts {
		if c = strings.TrimSpace(c); c != "" {
			out.Comfort = c
			break
		}
	}
	if out == (QueueAudioSummary{}) {
		return nil
	}
	return &out
}

// --- find_audio_usage ---------------------------------------------------------

// AudioUsage is one place an audio file is named.
type AudioUsage struct {
	// Kind is what names it: queue, digital receptionist, ring group, or the
	// system's music on hold.
	Kind   string `json:"kind"`
	Number string `json:"number,omitempty"`
	Name   string `json:"name,omitempty"`
	// Setting is the setting it is used for, in the words the console uses.
	Setting string `json:"setting"`
}

// AudioUsageResult is everything that names one file.
type AudioUsageResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string `json:"customer"`
	Filename string `json:"filename"`
	// Exists reports whether the phone system has a file by that name at all.
	// Nil when the file list could not be read, which is not the same as the
	// file being absent.
	Exists *bool        `json:"exists"`
	Usages []AudioUsage `json:"usages"`
	// Searched names what was looked through, so "nothing uses it" says what
	// that covers.
	Searched []string `json:"searched"`
	// Unavailable names what this build of 3CX would not answer for. While it
	// has anything in it, "nothing uses this file" is not a conclusion that can
	// be drawn, and Summary says so.
	Unavailable []string `json:"unavailable,omitempty"`
	// Summary is the finding in one sentence, because the three cases -- no
	// such file, an unused file, and a question that could not be fully asked
	// -- read almost alike from the fields alone.
	Summary string `json:"summary"`
}

type audioUsageArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Filename string `json:"filename" jsonschema:"the audio file's name, as list_music_on_hold reports it"`
}

func (p *Plugin) searchAudioUsage(ctx context.Context, args audioUsageArgs) (AudioUsageResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return AudioUsageResult{}, err
	}
	wanted := strings.TrimSpace(args.Filename)
	if wanted == "" {
		return AudioUsageResult{}, fmt.Errorf("filename is required: the audio file's " +
			"name, as list_music_on_hold reports it")
	}
	out := AudioUsageResult{Filename: wanted, Usages: []AudioUsage{}, Searched: []string{}}

	// Whether the file exists at all. Asked first, because "no such file" is a
	// different answer from "nothing uses it" and the second is misleading
	// when the first is true.
	if exists, err := p.audioFileExists(ctx, acct, wanted); err == nil {
		out.Exists = &exists
	} else if missing(err) {
		out.Unavailable = append(out.Unavailable, "the list of audio files, so whether it exists is not known")
	} else {
		return AudioUsageResult{}, acct.call(err)
	}

	for _, src := range p.audioSources() {
		usages, err := src.find(p, ctx, acct, wanted)
		switch {
		case err != nil && missing(err):
			out.Unavailable = append(out.Unavailable, src.what)
			continue
		case err != nil:
			return AudioUsageResult{}, acct.call(err)
		}
		out.Searched = append(out.Searched, src.what)
		out.Usages = append(out.Usages, usages...)
	}

	out.Summary = summariseUsage(out)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// audioSource is one place audio files are named.
//
// find is a method expression, so the receiver is its first parameter: the
// four searches differ only in what they read, and a table of them keeps the
// "which of these did this build refuse" bookkeeping in one place.
type audioSource struct {
	// what is the thing searched, in the words the summary uses.
	what string
	find func(*Plugin, context.Context, *account, string) ([]AudioUsage, error)
}

func (p *Plugin) audioSources() []audioSource {
	return []audioSource{
		{"queues", (*Plugin).findInQueues},
		{"digital receptionists", (*Plugin).findInReceptionists},
		{"ring groups", (*Plugin).findInRingGroups},
		{"the system's music on hold", (*Plugin).findInSystemMusic},
	}
}

func (p *Plugin) findInQueues(ctx context.Context, acct *account, wanted string) ([]AudioUsage, error) {
	q := url.Values{
		"$select":  {"Id,Number,Name," + queueAudioFields},
		"$orderby": {"Number"},
	}
	got, err := list[queueRecord](ctx, acct.client, "Queues", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	var out []AudioUsage
	for _, qu := range got.Rows {
		at := func(setting, value string) {
			if sameAudioFile(value, wanted) {
				out = append(out, AudioUsage{Kind: "queue", Number: qu.Number, Name: qu.Name, Setting: setting})
			}
		}
		at("music on hold", qu.OnHoldFile)
		at("prompt set", qu.PromptSet)
		at("intro prompt", qu.IntroFile)
		at("greeting", qu.GreetingFile)
		at("callback offer prompt", qu.CallbackOfferPrompt)
		at("prompt when nobody answers", qu.NoAnswerPrompt)
		for _, c := range qu.ComfortPrompts {
			at("comfort prompt", c)
		}
	}
	return out, nil
}

func (p *Plugin) findInReceptionists(ctx context.Context, acct *account, wanted string) ([]AudioUsage, error) {
	q := url.Values{
		"$select":  {"Id,Number,Name,PromptFilename,PromptSet"},
		"$orderby": {"Number"},
	}
	got, err := list[struct {
		Number string `json:"Number"`
		Name   string `json:"Name"`
		Prompt string `json:"PromptFilename"`
		Set    string `json:"PromptSet"`
	}](ctx, acct.client, "Receptionists", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	var out []AudioUsage
	for _, r := range got.Rows {
		if sameAudioFile(r.Prompt, wanted) {
			out = append(out, AudioUsage{Kind: "digital receptionist", Number: r.Number, Name: r.Name, Setting: "menu prompt"})
		}
		if sameAudioFile(r.Set, wanted) {
			out = append(out, AudioUsage{Kind: "digital receptionist", Number: r.Number, Name: r.Name, Setting: "prompt set"})
		}
	}
	return out, nil
}

func (p *Plugin) findInRingGroups(ctx context.Context, acct *account, wanted string) ([]AudioUsage, error) {
	q := url.Values{
		"$select":  {"Id,Number,Name,GreetingFile"},
		"$orderby": {"Number"},
	}
	got, err := list[struct {
		Number   string `json:"Number"`
		Name     string `json:"Name"`
		Greeting string `json:"GreetingFile"`
	}](ctx, acct.client, "RingGroups", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	var out []AudioUsage
	for _, g := range got.Rows {
		if sameAudioFile(g.Greeting, wanted) {
			out = append(out, AudioUsage{Kind: "ring group", Number: g.Number, Name: g.Name, Setting: "greeting"})
		}
	}
	return out, nil
}

func (p *Plugin) findInSystemMusic(ctx context.Context, acct *account, wanted string) ([]AudioUsage, error) {
	sys, err := p.readSystemMusicOnHold(ctx, acct)
	if err != nil {
		return nil, err
	}
	var out []AudioUsage
	for _, s := range sys.slots() {
		if sameAudioFile(s.File, wanted) {
			out = append(out, AudioUsage{
				Kind:    "the system's music on hold",
				Setting: fmt.Sprintf("slot %d", s.Slot),
			})
		}
	}
	return out, nil
}

// audioFileExists reports whether the phone system holds a file by that name.
func (p *Plugin) audioFileExists(ctx context.Context, acct *account, wanted string) (bool, error) {
	q := url.Values{"$select": {"Filename,DisplayName"}, "$orderby": {"Filename"}}
	got, err := list[struct {
		Filename    string `json:"Filename"`
		DisplayName string `json:"DisplayName"`
	}](ctx, acct.client, "CustomPrompts", q, p.cfg.MaxItems)
	if err != nil {
		return false, err
	}
	for _, f := range got.Rows {
		if sameAudioFile(f.Filename, wanted) {
			return true, nil
		}
	}
	return false, nil
}

// summariseUsage says which of the three findings this is.
//
// They are close enough in the fields alone to be read for one another: a file
// that does not exist, one that exists and nothing uses, and a search that
// could not cover everything. The last is the one worth being loud about,
// because "nothing uses it" is the sentence somebody deletes a file on.
func summariseUsage(r AudioUsageResult) string {
	switch {
	case len(r.Usages) > 0:
		where := fmt.Sprintf("%d place", len(r.Usages))
		if len(r.Usages) != 1 {
			where += "s"
		}
		out := fmt.Sprintf("%s is used in %s.", r.Filename, where)
		if len(r.Unavailable) > 0 {
			out += " There may be more: this phone system would not answer for " +
				englishList(r.Unavailable) + "."
		}
		return out
	case len(r.Unavailable) > 0:
		return fmt.Sprintf("Nothing found using %s, but this is not a complete answer: "+
			"this phone system would not answer for %s. Do not treat %s as unused.",
			r.Filename, englishList(r.Unavailable), r.Filename)
	case r.Exists != nil && !*r.Exists:
		return fmt.Sprintf("There is no audio file called %s on this phone system. "+
			"list_music_on_hold has the files it does have.", r.Filename)
	default:
		return fmt.Sprintf("%s exists and nothing in %s uses it.",
			r.Filename, englishList(r.Searched))
	}
}

// englishList joins names the way a sentence does.
func englishList(items []string) string {
	switch len(items) {
	case 0:
		return "nothing"
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// audioKey is the form two audio filenames are compared in.
//
// 3CX stores a prompt sometimes bare and sometimes with the folder it sits in,
// and the console shows the bare name. Comparing what it shows against what it
// stores has to fold both to the same thing, or a file plainly in use reads as
// unused.
func audioKey(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	n = strings.ReplaceAll(n, `\`, "/")
	// path.Base answers "." for an empty path and "/" for a bare separator,
	// neither of which is a filename. Left alone, two fields that are both
	// unset compare equal and every queue that names no music reads as using
	// whatever was asked about.
	base := strings.TrimSpace(path.Base(n))
	if base == "." || base == "/" {
		return ""
	}
	return strings.ToLower(base)
}

func sameAudioFile(a, b string) bool {
	ka, kb := audioKey(a), audioKey(b)
	return ka != "" && ka == kb
}
