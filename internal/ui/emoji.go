package ui

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stevedylandev/whisk/internal/protocol"
)

// The emoji picker: `+` on a selected message opens a search field, and the
// user types a name (or alias) to find an emoji to react with. `/emoji` opens
// the same picker from the composer and inserts the choice into the message
// instead. The catalog comes from emoji_data.go. Results show each emoji's
// native short name, and matching is word-based fuzzy search, so "thumbs",
// "thumbsup", or "th up" find 👍 without letting scattered letters match
// unrelated emoji.
//
// Flag pairs (two regional indicators) draw as a fixed-width flag marker:
// terminals disagree on their advance width, which tears the picker's border
// and shifts the name. The country's short name still identifies it, and the
// reaction sends the real flag.

// emojiEntry is one searchable emoji.
type emojiEntry struct {
	char    string   // the emoji as sent in reactions
	display string   // the emoji width-safe for layout
	label   string   // native short name, e.g. "thumbsup"
	name    string   // lower-case description, e.g. "thumbs up"
	aliases string   // lower-case short names, e.g. "+1 thumbsup"
	words   []string // lower-case description, alias, tag and category words
}

// emojiPicker is the picker's state. msgID is fixed when the picker opens, so
// a message arriving while the user types cannot move the reaction. insert
// marks the /emoji variant, which puts the choice in the composer instead.
type emojiPicker struct {
	open   bool
	insert bool
	msgID  string
	query  string
	cursor int
}

var (
	emojiOnce    sync.Once
	emojiCatalog []emojiEntry
	emojiDefault []emojiEntry
	emojiRank    map[string]int
	// emojiPopular ranks familiar emoji first, both before the user types and
	// as a tie-breaker in search results. Entries are aliases.
	emojiPopular = []string{
		"+1", "heart", "joy", "tada", "smile", "cry", "open_mouth", "pray",
		"eyes", "fire", "white_check_mark", "clap", "100", "thinking",
		"sweat_smile", "heart_eyes", "rocket", "partying_face", "wave",
		"muscle", "raised_hands", "sob", "laughing", "wink",
	}
)

// emojiDisplay returns the glyph drawn in the picker. Regional flag pairs
// vary in advance width across terminals, so they get a text flag marker that
// keeps the same two-cell column as an ordinary emoji.
func emojiDisplay(s string) string {
	if isRegionalFlag(s) {
		return "⚑ "
	}
	return termWidthSafe(s)
}

func isRegionalFlag(s string) bool {
	r := []rune(s)
	return len(r) == 2 && isRegionalIndicator(r[0]) && isRegionalIndicator(r[1])
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// loadEmoji parses the embedded catalog once and builds the empty-query list.
func loadEmoji() {
	emojiOnce.Do(func() {
		lines := strings.Split(emojiData, "\n")
		emojiCatalog = make([]emojiEntry, 0, len(lines))
		byAlias := make(map[string]emojiEntry, len(lines))
		for _, line := range lines {
			parts := strings.SplitN(line, "\t", 4)
			if len(parts) != 4 || parts[0] == "" {
				continue
			}
			aliases := strings.Fields(strings.ToLower(parts[2]))
			label := parts[1]
			if len(aliases) > 0 {
				label = aliases[0]
			}
			e := emojiEntry{
				char: parts[0],
				// VS16 and ZWJ make lipgloss and the terminal disagree on an
				// emoji's width, which tears the picker's border (see
				// termWidthSafe). Strip them from the drawn copy before any
				// padding is measured; reactions still send the full sequence.
				display: emojiDisplay(parts[0]),
				label:   label,
				name:    strings.ToLower(parts[1]),
				aliases: strings.Join(aliases, " "),
				words:   emojiWords(parts[1] + " " + parts[2] + " " + parts[3]),
			}
			emojiCatalog = append(emojiCatalog, e)
			for _, alias := range aliases {
				byAlias[alias] = e
			}
		}

		emojiRank = make(map[string]int, len(emojiPopular))
		seen := make(map[string]bool, len(emojiCatalog))
		for rank, alias := range emojiPopular {
			e, ok := byAlias[alias]
			if !ok {
				continue
			}
			if _, ok := emojiRank[e.char]; !ok {
				emojiRank[e.char] = rank
			}
			if !seen[e.char] {
				seen[e.char] = true
				emojiDefault = append(emojiDefault, e)
			}
		}
		for _, e := range emojiCatalog {
			if !seen[e.char] {
				seen[e.char] = true
				emojiDefault = append(emojiDefault, e)
			}
		}
	})
}

// normalizeEmojiQuery trims the colons users often type around an alias and
// lower-cases the rest.
func normalizeEmojiQuery(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	return strings.Trim(q, ":")
}

// emojiMatches returns the catalog entries matching query, best first. An
// empty query lists the popular emoji first and then the rest of the catalog.
func emojiMatches(query string) []emojiEntry {
	loadEmoji()
	q := normalizeEmojiQuery(query)
	if q == "" {
		return emojiDefault
	}

	tokens := emojiQueryTokens(q)
	if len(tokens) == 0 {
		return emojiDefault
	}

	type hit struct {
		e     emojiEntry
		score int
	}
	hits := make([]hit, 0, 64)
	for _, e := range emojiCatalog {
		total := 0
		ok := true
		for _, token := range tokens {
			score, matched := scoreEmojiToken(token, e)
			if !matched {
				ok = false
				break
			}
			total += score
		}
		if !ok {
			continue
		}
		if rank, ok := emojiRank[e.char]; ok {
			total += (len(emojiPopular) - rank) * 20
		}
		hits = append(hits, hit{e: e, score: total})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if len(hits[i].e.label) != len(hits[j].e.label) {
			return len(hits[i].e.label) < len(hits[j].e.label)
		}
		return hits[i].e.label < hits[j].e.label
	})

	out := make([]emojiEntry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// scoreEmojiToken scores one lower-case query token against an entry. Words
// must match individually: that keeps "sungla" away from unrelated emoji
// whose scattered letters happen to spell it across several words.
func scoreEmojiToken(token string, e emojiEntry) (int, bool) {
	best, matched := 0, false
	add := func(score int) {
		if !matched || score > best {
			best, matched = score, true
		}
	}

	if e.name == token {
		add(1400)
	}
	if hasEmojiAlias(e.aliases, token) {
		add(1300)
	}
	if strings.HasPrefix(strings.ToLower(e.label), token) {
		add(1150)
	}
	for _, w := range e.words {
		switch {
		case w == token:
			add(1200)
		case strings.HasPrefix(w, token):
			add(1000 - len(w))
		case strings.Contains(w, token):
			add(800 - strings.Index(w, token))
		case len([]rune(token)) >= 3:
			// Allow small typos within one word, never across words.
			if gap, ok := subsequenceGap(token, w); ok {
				add(400 - gap)
			}
		}
	}
	return best, matched
}

// emojiWords splits a lower-case search text into words at spaces, hyphens,
// underscores and other punctuation.
func emojiWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// emojiQueryTokens splits what the user typed so a native short name with
// underscores or hyphens ("dark_sunglasses", "thumbs-up") works too.
func emojiQueryTokens(q string) []string {
	return strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || r == '_' || r == '-'
	})
}

// hasEmojiAlias reports whether token is one of the whitespace-separated
// short names. It keeps punctuation such as "+1".
func hasEmojiAlias(aliases, token string) bool {
	for _, alias := range strings.Fields(aliases) {
		if alias == token {
			return true
		}
	}
	return false
}

// subsequenceGap reports whether every rune of token appears in text in
// order, and how many characters were skipped doing so.
func subsequenceGap(token, text string) (int, bool) {
	gap := 0
	from := 0
	last := -1
	for _, r := range token {
		idx := strings.IndexRune(text[from:], r)
		if idx < 0 {
			return 0, false
		}
		if last >= 0 {
			gap += idx
		}
		last = from + idx
		from = last + len(string(r))
	}
	return gap, true
}

// openEmojiPicker opens the picker for the selected message.
func (m *Model) openEmojiPicker() {
	if !m.requireCap("reactions") {
		return
	}
	msg := m.selectedMessage()
	if msg == nil {
		m.setStatus("no message selected", true)
		return
	}
	m.chat.emoji = emojiPicker{open: true, msgID: msg.MessageID}
}

// openEmojiInsert opens the picker from /emoji to put the chosen emoji in the
// composer. Inserting text needs no selected message or reactions capability.
func (m *Model) openEmojiInsert(query string) {
	m.setFocus(focusComposer)
	m.chat.emoji = emojiPicker{open: true, insert: true}
	m.chat.emoji.setQuery(query)
}

// handleEmojiKey handles every key while the picker is open. Printable keys
// extend the query, so j/k are typed rather than used for navigation.
func (m *Model) handleEmojiKey(key tea.KeyMsg) []tea.Cmd {
	p := &m.chat.emoji
	switch key.Type {
	case tea.KeyEsc:
		*p = emojiPicker{}
		return nil
	case tea.KeyEnter:
		results := emojiMatches(p.query)
		if len(results) == 0 {
			return nil
		}
		if p.cursor >= len(results) {
			p.cursor = len(results) - 1
		}
		choice, id, insert := results[p.cursor], p.msgID, p.insert
		*p = emojiPicker{}
		if insert {
			m.chat.composer.InsertString(choice.char)
			m.setFocus(focusComposer)
			return m.maybeSendTyping()
		}
		if cmd := m.reactTo(id, choice.char); cmd != nil {
			return []tea.Cmd{cmd}
		}
		return nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.cursor > 0 {
			p.cursor--
		}
		return nil
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if n := len(emojiMatches(p.query)); p.cursor < n-1 {
			p.cursor++
		}
		return nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		p.setQuery(trimLastEmojiRune(p.query))
		return nil
	case tea.KeyCtrlU:
		p.setQuery("")
		return nil
	case tea.KeySpace:
		p.setQuery(p.query + " ")
		return nil
	case tea.KeyRunes:
		p.setQuery(p.query + string(key.Runes))
		return nil
	}
	return nil
}

// setQuery changes the search text and puts the cursor back on the best hit.
func (p *emojiPicker) setQuery(q string) {
	p.query = q
	p.cursor = 0
}

func trimLastEmojiRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[:len(r)-1])
}

// reactTo adds emoji to, or removes it from, the user's reactions on one
// message. A reactions request carries the complete set (§4.7), and changes
// count as posting: a user who may only read (ext:permissions) cannot react.
func (m *Model) reactTo(messageID, emoji string) tea.Cmd {
	if !m.requireCap("reactions") || !m.requireWrite() {
		return nil
	}
	store := m.cur().store
	msg := store.MessageByID(messageID)
	if msg == nil {
		m.setStatus("no message selected", true)
		return nil
	}
	if store.You == nil {
		return nil
	}
	mine := store.MyReactions(msg.MessageID, store.You.UserID)
	return requestCmd(m.cur(), "reactions", "reactions", protocol.ReactionsRequest{
		MessageID: msg.MessageID,
		Emojis:    toggleEmoji(mine, emoji),
	})
}
