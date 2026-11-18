package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/player"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	"github.com/sheyiyuan/zero-beat-tui/internal/keymap"
)

type contentKind int

const (
	contentComments contentKind = iota
	contentRelated
)

// 评论按页展示并逐行滚动；推荐按条目选择。请求序号由 Model 统一递增，
// 关闭重开同一首或翻页后，迟到的结果也不能覆盖当前面板。
type contentPopup struct {
	kind                  contentKind
	song                  store.Song
	page, requestPage     int
	cursor                int
	busy, hasMore         bool
	comments              []source.Comment
	related               []source.Candidate
	err, notice, feedback string
	request               int
	cancel                context.CancelFunc
}

type commentsMsg struct {
	songID        string
	page, request int
	result        source.CommentPage
	err           error
}
type relatedMsg struct {
	songID  string
	request int
	items   []source.Candidate
	err     error
}

func (m *Model) supportsContent(song store.Song, kind contentKind) bool {
	src := m.sources.GetString(song.Source)
	if src == nil {
		return false
	}
	if kind == contentComments {
		_, ok := src.(source.CommentReader)
		return ok && src.Caps().Has(source.CapComments)
	}
	_, ok := src.(source.RelatedRecommender)
	return ok && src.Caps().Has(source.CapRelated)
}

func (m *Model) closeContent() {
	if m.content != nil && m.content.cancel != nil {
		m.content.cancel()
	}
	m.content = nil
}

func (m *Model) openSelectedContent(kind contentKind) tea.Cmd {
	song, ok := m.selectedDownloadSong()
	if m.view == viewLyrics && m.current != nil {
		song, ok = *m.current, true
	}
	if !ok {
		m.setNotice("当前页面没有选中的歌曲", true)
		return nil
	}
	return m.openContent(song, kind)
}

func (m *Model) openComments(song store.Song) tea.Cmd { return m.openContent(song, contentComments) }
func (m *Model) openRelated(song store.Song) tea.Cmd  { return m.openContent(song, contentRelated) }
func (m *Model) openContent(song store.Song, kind contentKind) tea.Cmd {
	m.closeContent()
	m.content = &contentPopup{kind: kind, song: store.NormalizeSong(song), page: 1}
	return m.requestContent(1)
}

func (m *Model) requestContent(page int) tea.Cmd {
	c := m.content
	if c == nil {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	m.contentRequest++
	c.request, c.requestPage = m.contentRequest, max(1, page)
	c.err, c.feedback = "", ""
	if !m.supportsContent(c.song, c.kind) {
		c.busy = false
		c.err = "当前音源不支持" + c.title()
		return nil
	}
	c.busy = true
	parent := m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	c.cancel = cancel
	// 后台命令只捕获不可变快照，不读取 Model 或弹层指针。
	song, request, requestedPage := c.song, c.request, c.requestPage
	src := m.sources.GetString(song.Source)
	if c.kind == contentComments {
		reader := src.(source.CommentReader)
		return func() tea.Msg {
			defer cancel()
			result, err := reader.Comments(ctx, app.RefFromSong(song), requestedPage)
			return commentsMsg{songID: song.ID, page: requestedPage, request: request, result: result, err: err}
		}
	}
	recommender := src.(source.RelatedRecommender)
	return func() tea.Msg {
		defer cancel()
		items, err := recommender.Related(ctx, app.RefFromSong(song))
		return relatedMsg{songID: song.ID, request: request, items: items, err: err}
	}
}

func (m *Model) handleContentMsg(msg tea.Msg) (tea.Cmd, bool) {
	c := m.content
	switch msg := msg.(type) {
	case commentsMsg:
		if c == nil || c.kind != contentComments || !c.busy || msg.songID != c.song.ID || msg.request != c.request {
			return nil, true
		}
		c.busy = false
		if msg.err != nil {
			c.err = msg.err.Error()
		} else {
			c.err = ""
			c.comments, c.page = msg.result.Comments, msg.page
			c.hasMore, c.notice = msg.result.HasMore && len(c.comments) > 0, msg.result.Notice
			c.cursor = 0
		}
		return nil, true
	case relatedMsg:
		if c == nil || c.kind != contentRelated || !c.busy || msg.songID != c.song.ID || msg.request != c.request {
			return nil, true
		}
		c.busy = false
		if msg.err != nil {
			c.err = msg.err.Error()
		} else {
			c.err = ""
			c.related, c.cursor = msg.items, 0
		}
		return nil, true
	}
	return nil, false
}

func (c *contentPopup) title() string {
	if c.kind == contentComments {
		return "评论区"
	}
	return "相关推荐"
}

func (m *Model) handleContentKey(key tea.Key) (tea.Model, tea.Cmd) {
	c := m.content
	if c == nil {
		return m, nil
	}
	switch {
	case isCtrl(key, 'c'):
		m.closeContent()
		m.shutdown()
		return m, tea.Quit
	case key.Code == tea.KeyEscape || m.match(keymap.Back, key):
		m.closeContent()
		return m, nil
	case m.match(keymap.Profiles, key):
		return m, m.openCurrentProfiles()
	case key.Code == tea.KeyTab:
		other := contentRelated
		if c.kind == contentRelated {
			other = contentComments
		}
		if m.supportsContent(c.song, other) {
			return m, m.openContent(c.song, other)
		}
		return m, nil
	}
	if c.busy {
		return m, nil
	}
	switch {
	case c.kind == contentComments && m.match(keymap.CommentAuthor, key):
		_, authors := m.commentLinesAndAuthors()
		if len(authors) > 0 {
			ref := authors[clampInt(c.cursor, 0, len(authors)-1)]
			if ref.ID != "" && m.browseSource(ref.Source) != nil {
				m.closeContent()
				return m, m.enterBrowse(ref)
			}
		}
		c.feedback = "当前评论没有可用的作者主页"
		return m, nil
	case m.match(keymap.Refresh, key):
		return m, m.requestContent(c.requestPage)
	case m.match(keymap.Up, key):
		m.scrollContent(-1)
	case m.match(keymap.Down, key):
		m.scrollContent(1)
	case m.match(keymap.PageUp, key):
		m.scrollContent(-m.contentBodyHeight())
	case m.match(keymap.PageDown, key):
		m.scrollContent(m.contentBodyHeight())
	case m.match(keymap.Top, key) || key.Code == tea.KeyHome:
		c.cursor = 0
	case m.match(keymap.Bottom, key) || key.Code == tea.KeyEnd:
		m.scrollContent(m.contentLength())
	case c.kind == contentComments && key.Text == "[" && c.page > 1:
		return m, m.requestContent(c.page - 1)
	case c.kind == contentComments && key.Text == "]" && c.hasMore:
		return m, m.requestContent(c.page + 1)
	}
	if c.kind != contentRelated || len(c.related) == 0 {
		return m, nil
	}
	c.cursor = clampInt(c.cursor, 0, len(c.related)-1)
	song := app.SongFromTrack(c.related[c.cursor].Track)
	switch {
	case key.Code == tea.KeyEnter || m.match(keymap.PlaySelected, key):
		song = m.ensureStored(song)
		m.withController(func(p *player.Controller) { p.SetIndex(p.Enqueue(song)) })
		m.queueCursor = m.queueIdx
		m.setPlayingPlaylist("")
		m.persistQueue()
		m.closeContent()
		m.closeBrowse()
		return m, m.playSong(song)
	case m.match(keymap.Enqueue, key):
		cmd := m.enqueueSong(song)
		c.feedback = "已加入队列：" + song.Title
		return m, cmd
	case m.match(keymap.PlayNext, key):
		cmd := m.insertNext(song)
		c.feedback = m.note.text
		return m, cmd
	case m.match(keymap.AddPlaylist, key):
		m.closeContent()
		m.closeBrowse()
		return m, m.openAddSongPicker(song)
	}
	return m, nil
}

func (m *Model) contentWidth() int      { return min(m.width, max(4, min(96, m.width-4))) }
func (m *Model) contentBodyHeight() int { return max(1, m.height-7) }

// plainContent 防止平台内容中的终端转义/控制字符破坏整屏，保留换行。
func plainContent(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func contentOneLine(s string) string { return strings.Join(strings.Fields(plainContent(s)), " ") }

func (m *Model) commentLines() []string { lines, _ := m.commentLinesAndAuthors(); return lines }
func (m *Model) commentLinesAndAuthors() ([]string, []source.BrowseRef) {
	c := m.content
	var out []string
	var authors []source.BrowseRef
	width := max(1, m.contentWidth()-3)
	for _, comment := range c.comments {
		meta := fmt.Sprintf("%s · %d 赞", contentOneLine(comment.Author), comment.Likes)
		if !comment.CreatedAt.IsZero() {
			meta += " · " + comment.CreatedAt.Local().Format("2006-01-02")
		}
		if comment.ReplyCount > 0 {
			meta += fmt.Sprintf(" · %d 回复", comment.ReplyCount)
		}
		for _, line := range wrapText(meta, width) {
			out = append(out, st.accent.Render(line))
		}
		for _, paragraph := range strings.Split(plainContent(comment.Content), "\n") {
			lines := wrapText(paragraph, width)
			if len(lines) == 0 {
				lines = []string{""}
			}
			for _, line := range lines {
				out = append(out, st.text.Render(line))
			}
		}
		out = append(out, "")
		for len(authors) < len(out) {
			authors = append(authors, comment.AuthorRef)
		}
	}
	return out, authors
}

func (m *Model) contentLength() int {
	if m.content.kind == contentComments {
		return len(m.commentLines())
	}
	return len(m.content.related)
}
func (m *Model) scrollContent(delta int) {
	if m.content == nil {
		return
	}
	limit := m.contentLength() - 1
	if m.content.kind == contentComments {
		limit = m.contentLength() - m.contentBodyHeight()
	}
	m.content.cursor = clampInt(m.content.cursor+delta, 0, max(0, limit))
}

func (m *Model) contentLines() []string {
	c := m.content
	if c == nil {
		return nil
	}
	width, bodyH := m.contentWidth(), m.contentBodyHeight()
	inner, textW := width-2, max(1, width-3)
	out := []string{boxTop(truncate(c.title()+" · "+contentOneLine(c.song.Title), max(1, width-4)), width)}
	subtitle := "↑/↓ 滚动 · [ / ] 翻页 · Tab 相关推荐"
	if c.kind == contentRelated {
		subtitle = "↑/↓ 选择 · " + m.firstKey(keymap.Profiles) + " 当前播放作者 · Tab 评论区"
	}
	out = append(out, boxLine(truncate(st.muted.Render(subtitle), textW), inner))
	var rows []string
	status := ""
	if c.kind == contentComments {
		rows = m.commentLines()
		start := clampInt(c.cursor, 0, max(0, len(rows)-bodyH))
		status = fmt.Sprintf("第 %d 页 · %d 条", c.page, len(c.comments))
		if c.hasMore {
			status += " · ] 下一页"
		}
		if c.notice != "" {
			status += " · " + c.notice
		}
		rows = rows[start:min(len(rows), start+bodyH)]
	} else {
		cursor := clampInt(c.cursor, 0, max(0, len(c.related)-1))
		start := max(0, cursor-bodyH+1)
		status = fmt.Sprintf("%d 条推荐", len(c.related))
		for i := start; i < min(len(c.related), start+bodyH); i++ {
			item := c.related[i]
			line := fmt.Sprintf("%d. %s · %s", i+1, contentOneLine(item.Title), contentOneLine(item.Artist))
			if item.VIP {
				line += " · VIP"
			}
			if item.Duration > 0 {
				line += " · " + formatDuration(time.Duration(item.Duration)*time.Second)
			}
			line = truncate(line, textW)
			if i == cursor {
				line = st.selected.Render(padRight(line, textW))
			}
			rows = append(rows, line)
		}
	}
	if len(rows) == 0 {
		empty := "暂无评论"
		if c.kind == contentRelated {
			empty = "暂无相关推荐"
		}
		if c.busy {
			empty = "加载中…"
		} else if c.err != "" {
			empty = "加载失败，按 r 重试"
		}
		rows = []string{st.muted.Render(empty)}
	}
	for len(rows) < bodyH {
		rows = append(rows, "")
	}
	for _, line := range rows {
		out = append(out, boxLine(truncate(line, textW), inner))
	}
	if c.feedback != "" {
		status = c.feedback
	}
	if c.busy {
		status = "加载中…"
	}
	if c.err != "" {
		status = "加载失败：" + contentOneLine(c.err) + " · r 重试"
	}
	out = append(out, boxLine(st.warn.Render(truncate(status, textW)), inner))
	hint := m.firstKey(keymap.Profiles) + " 当前作者 · " + m.firstKey(keymap.CommentAuthor) + " 评论作者 · r 刷新 · esc 关闭"
	if c.kind == contentRelated {
		hint = "enter 播放 · " + m.firstKey(keymap.Enqueue) + " 队列 · " + m.firstKey(keymap.PlayNext) + " 下一首 · " + m.firstKey(keymap.AddPlaylist) + " 歌单 · r 刷新 · esc 关闭"
	}
	out = append(out, boxLine(st.dim.Render(truncate(hint, textW)), inner), boxBottom(width))
	return out
}
