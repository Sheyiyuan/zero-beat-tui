package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	"github.com/sheyiyuan/zero-beat-tui/internal/keymap"
	"strings"
	"time"
)

type browseState struct {
	ref                 source.BrowseRef
	song                *store.Song // 首层为歌曲关联的歌手/UP 主列表
	authorsOnly         bool        // 当前播放作者入口仅包含人物，不把所属电台计为作者
	result              source.BrowsePage
	cursor, requestPage int
	busy                bool
	err, feedback       string
	request             int
	cancel              context.CancelFunc
	history             []*browseState
}
type browseMsg struct {
	request int
	result  source.BrowsePage
	err     error
}

func (m *Model) closeBrowse() {
	if m.browse != nil && m.browse.cancel != nil {
		m.browse.cancel()
	}
	m.browse = nil
}
func (m *Model) browseSource(id source.ID) source.Browser {
	s := m.sources.Get(id)
	if s == nil || !s.Caps().Has(source.CapBrowse) {
		return nil
	}
	b, _ := s.(source.Browser)
	return b
}
func (m *Model) openProfiles(song store.Song) tea.Cmd {
	m.closeBrowse()
	return m.enterProfiles(song)
}
func (m *Model) enterProfiles(song store.Song) tea.Cmd {
	return m.pushBrowse(&browseState{song: &song, ref: source.BrowseRef{Source: source.ID(song.Source)}, result: source.BrowsePage{Title: "歌手 / 用户主页"}})
}

// openCurrentProfiles 固定使用按键时的当前播放曲目，不随列表焦点变化。
func (m *Model) openCurrentProfiles() tea.Cmd {
	if m.current == nil {
		message := "当前没有正在播放的歌曲"
		m.setNotice(message, true)
		if m.content != nil {
			m.content.feedback = message
		}
		if m.browse != nil {
			m.browse.feedback = message
		}
		return nil
	}
	song := *m.current
	m.closeContent()
	return m.pushBrowse(&browseState{song: &song, authorsOnly: true, ref: source.BrowseRef{Source: source.ID(song.Source)}, result: source.BrowsePage{Title: "当前播放 · 作者"}})
}
func (m *Model) openSelectedProfiles() tea.Cmd {
	song, ok := m.selectedDownloadSong()
	if m.view == viewLyrics && m.current != nil {
		song, ok = *m.current, true
	}
	if !ok {
		m.setNotice("当前页面没有选中的歌曲", true)
		return nil
	}
	return m.openProfiles(song)
}
func (m *Model) openBrowse(ref source.BrowseRef) tea.Cmd {
	m.closeBrowse()
	m.browse = &browseState{ref: ref, result: source.BrowsePage{Title: "内容浏览"}}
	return m.requestBrowse(1)
}
func (m *Model) enterBrowse(ref source.BrowseRef) tea.Cmd {
	return m.pushBrowse(&browseState{ref: ref, result: source.BrowsePage{Title: "内容浏览"}})
}
func (m *Model) pushBrowse(next *browseState) tea.Cmd {
	if old := m.browse; old != nil {
		if old.cancel != nil {
			old.cancel()
		}
		next.history = append(append([]*browseState{}, old.history...), old)
	}
	m.browse = next
	return m.requestBrowse(1)
}
func (m *Model) requestBrowse(page int) tea.Cmd {
	b := m.browse
	if b == nil {
		return nil
	}
	if b.cancel != nil {
		b.cancel()
	}
	m.browseRequest++
	b.request = m.browseRequest
	b.requestPage = max(1, page)
	b.err = ""
	b.feedback = ""
	src := m.browseSource(b.ref.Source)
	if src == nil {
		b.err = "此音源不支持主页浏览"
		b.busy = false
		return nil
	}
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	b.cancel = cancel
	b.busy = true
	ref, request, p, authorsOnly := b.ref, b.request, b.requestPage, b.authorsOnly
	var song *store.Song
	if b.song != nil {
		copy := *b.song
		song = &copy
	}
	return func() tea.Msg {
		defer cancel()
		var result source.BrowsePage
		var err error
		if song != nil {
			result.Title = "歌手 / 用户主页 · " + song.Title
			result.Page = 1
			result.Entries, err = src.TrackProfiles(ctx, app.RefFromSong(*song))
			if authorsOnly {
				result.Title = "当前播放 · 作者 · " + song.Title
				var authors []source.BrowseEntry
				seen := map[source.BrowseRef]bool{}
				for _, entry := range result.Entries {
					if entry.Track == nil && entry.Ref.ID != "" && (entry.Ref.Kind == source.BrowseArtist || entry.Ref.Kind == source.BrowseUser) && !seen[entry.Ref] {
						authors = append(authors, entry)
						seen[entry.Ref] = true
					}
				}
				result.Entries = authors
			}
		} else {
			result, err = src.Browse(ctx, ref, p)
		}
		return browseMsg{request: request, result: result, err: err}
	}
}
func (m *Model) handleBrowseMsg(msg tea.Msg) (tea.Cmd, bool) {
	v, ok := msg.(browseMsg)
	if !ok {
		return nil, false
	}
	b := m.browse
	if b == nil || !b.busy || b.request != v.request {
		return nil, true
	}
	b.busy = false
	if v.err != nil {
		b.err = v.err.Error()
		if v.result.Title != "" {
			b.result.Title = v.result.Title
		}
		return nil, true
	}
	b.result, b.cursor, b.err = v.result, 0, ""
	if b.song != nil && len(v.result.Entries) == 1 {
		entry := v.result.Entries[0]
		if entry.Track == nil && entry.Ref.ID != "" {
			// 跳过单项选择层，失败后重试目标主页，返回时也不出现多余的一层。
			m.browse = &browseState{ref: entry.Ref, history: b.history, result: source.BrowsePage{Title: entry.Title}}
			return m.requestBrowse(1), true
		}
	}
	return nil, true
}
func (m *Model) scrollBrowse(n int) {
	if m.browse != nil {
		m.browse.cursor = clampInt(m.browse.cursor+n, 0, max(0, len(m.browse.result.Entries)-1))
	}
}
func (m *Model) handleBrowseKey(key tea.Key) (tea.Model, tea.Cmd) {
	b := m.browse
	if b == nil {
		return m, nil
	}
	switch {
	case isCtrl(key, 'c'):
		m.closeBrowse()
		m.shutdown()
		return m, tea.Quit
	case m.match(keymap.Profiles, key):
		return m, m.openCurrentProfiles()
	case key.Code == tea.KeyEscape:
		m.closeBrowse()
		return m, nil
	case key.Code == tea.KeyBackspace || m.match(keymap.Back, key):
		if len(b.history) == 0 {
			m.closeBrowse()
		} else {
			if b.cancel != nil {
				b.cancel()
			}
			m.browse = b.history[len(b.history)-1]
			if m.browse.busy {
				// 父页在加载时被当前作者入口打断，返回后恢复请求。
				return m, m.requestBrowse(m.browse.requestPage)
			}
		}
		return m, nil
	}
	if b.busy {
		return m, nil
	}
	switch {
	case m.match(keymap.Refresh, key):
		return m, m.requestBrowse(b.requestPage)
	case m.match(keymap.Up, key):
		m.scrollBrowse(-1)
	case m.match(keymap.Down, key):
		m.scrollBrowse(1)
	case m.match(keymap.PageUp, key):
		m.scrollBrowse(-m.browseBodyHeight())
	case m.match(keymap.PageDown, key):
		m.scrollBrowse(m.browseBodyHeight())
	case m.match(keymap.Top, key) || key.Code == tea.KeyHome:
		b.cursor = 0
	case m.match(keymap.Bottom, key) || key.Code == tea.KeyEnd:
		m.scrollBrowse(len(b.result.Entries))
	case key.Text == "[" && b.result.Page > 1:
		return m, m.requestBrowse(b.result.Page - 1)
	case key.Text == "]" && b.result.HasMore:
		return m, m.requestBrowse(b.result.Page + 1)
	}
	if b.err != "" || len(b.result.Entries) == 0 {
		return m, nil
	}
	item := b.result.Entries[b.cursor]
	if item.Track == nil {
		if key.Code == tea.KeyEnter {
			return m, m.enterBrowse(item.Ref)
		}
		return m, nil
	}
	song := app.SongFromTrack(*item.Track)
	switch {
	case key.Code == tea.KeyEnter || m.match(keymap.PlaySelected, key):
		song = m.ensureStored(song)
		m.withController(func(p *player.Controller) { p.SetIndex(p.Enqueue(song)) })
		m.queueCursor = m.queueIdx
		m.setPlayingPlaylist("")
		m.persistQueue()
		m.closeBrowse()
		return m, m.playSong(song)
	case m.match(keymap.Enqueue, key):
		cmd := m.enqueueSong(song)
		b.feedback = "已加入队列：" + song.Title
		return m, cmd
	case m.match(keymap.PlayNext, key):
		cmd := m.insertNext(song)
		b.feedback = m.note.text
		return m, cmd
	case m.match(keymap.AddPlaylist, key):
		m.closeBrowse()
		return m, m.openAddSongPicker(song)
	case m.match(keymap.Comments, key):
		return m, m.openComments(song)
	}
	return m, nil
}
func (m *Model) browseBodyHeight() int { return max(1, m.height-9) }
func (m *Model) browseLines() []string {
	b := m.browse
	if b == nil {
		return nil
	}
	w := m.contentWidth()
	inner, tw := w-2, max(1, w-3)
	h := m.browseBodyHeight()
	out := []string{boxTop(truncate(contentOneLine(b.result.Title), max(1, w-4)), w), boxLine(truncate(contentOneLine(b.result.Description), tw), inner), boxLine(truncate("↑/↓ 选择 · [ / ] 翻页 · backspace 返回", tw), inner)}
	cursor := clampInt(b.cursor, 0, max(0, len(b.result.Entries)-1))
	start := max(0, cursor-h+1)
	var rows []string
	for i := start; i < min(len(b.result.Entries), start+h); i++ {
		e := b.result.Entries[i]
		prefix := "› "
		if e.Track != nil {
			prefix = "♪ "
		}
		line := prefix + contentOneLine(e.Title)
		if e.Subtitle != "" {
			line += " · " + contentOneLine(e.Subtitle)
		}
		line = truncate(line, tw)
		if i == cursor {
			line = st.selected.Render(padRight(line, tw))
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		empty := "暂无公开内容"
		if b.song != nil && b.authorsOnly {
			empty = "未找到可用的作者主页"
		}
		if b.busy {
			empty = "加载中…"
		} else if b.err != "" {
			empty = "加载失败，按 r 重试"
		}
		rows = []string{empty}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	for _, line := range rows {
		out = append(out, boxLine(line, inner))
	}
	status := fmt.Sprintf("第 %d 页 · %d 项", max(1, b.result.Page), len(b.result.Entries))
	if b.result.HasMore {
		status += " · ] 下一页"
	}
	if b.feedback != "" {
		status = b.feedback
	}
	if b.result.Notice != "" {
		status = b.result.Notice
	}
	if b.busy {
		status = "加载中…"
	}
	if b.err != "" {
		status = "加载失败：" + contentOneLine(b.err)
	}
	hint := "enter 打开/播放 · " + m.firstKey(keymap.Enqueue) + " 队列 · " + m.firstKey(keymap.PlayNext) + " 下一首 · " + m.firstKey(keymap.AddPlaylist) + " 歌单"
	out = append(out, boxLine(truncate(status, tw), inner), boxLine(truncate(hint, tw), inner), boxLine(truncate(m.firstKey(keymap.Profiles)+" 当前作者 · "+m.firstKey(keymap.Comments)+" 评论 · r 重试/刷新 · esc 关闭", tw), inner), boxBottom(w))
	return out
}
func (m *Model) browsePaletteAction(action paletteAction) (tea.Cmd, bool) {
	raw := string(action)
	if strings.HasPrefix(raw, "browse-root:") {
		parts := strings.SplitN(strings.TrimPrefix(raw, "browse-root:"), ":", 2)
		if len(parts) == 2 {
			return m.openBrowse(source.BrowseRef{Source: source.ID(parts[0]), Kind: source.BrowseKind(parts[1])}), true
		}
	}
	if strings.HasPrefix(raw, "browse-user:") {
		id := source.ID(strings.TrimPrefix(raw, "browse-user:"))
		s := m.sources.Get(id)
		if s != nil {
			return m.openBrowse(source.BrowseRef{Source: id, Kind: source.BrowseUser, ID: s.User().ID}), true
		}
	}
	return nil, false
}
