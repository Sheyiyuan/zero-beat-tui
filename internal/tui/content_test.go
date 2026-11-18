package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
)

type contentSource struct {
	*fakeSource
	comments func(context.Context, source.Ref, int) (source.CommentPage, error)
	related  func(context.Context, source.Ref) ([]source.Candidate, error)
}

func (s *contentSource) Caps() source.Capability {
	return s.fakeSource.Caps() | source.CapComments | source.CapRelated
}
func (s *contentSource) Comments(ctx context.Context, r source.Ref, p int) (source.CommentPage, error) {
	return s.comments(ctx, r, p)
}
func (s *contentSource) Related(ctx context.Context, r source.Ref) ([]source.Candidate, error) {
	return s.related(ctx, r)
}
func contentFixture() (*Model, *contentSource) {
	m := fixtureModel()
	s := &contentSource{fakeSource: &fakeSource{id: source.Bilibili, name: "Bilibili"}}
	s.comments = func(_ context.Context, r source.Ref, p int) (source.CommentPage, error) {
		return source.CommentPage{Page: p, HasMore: p < 2, Comments: []source.Comment{{ID: fmt.Sprint(p), Author: "读者", Content: fmt.Sprintf("%s 第 %d 页的评论", r.ExternalID, p), Likes: 3}}}, nil
	}
	s.related = func(_ context.Context, r source.Ref) ([]source.Candidate, error) {
		return []source.Candidate{{Track: source.Track{Source: r.Source, ExternalID: "BVrelated", Page: 1, Title: "推荐曲目", Artist: "UP", Duration: 123}}}, nil
	}
	m.sources = source.NewRegistry(s)
	return m, s
}
func contentKey(m *Model, k tea.Key) tea.Cmd { _, cmd := m.Update(tea.KeyPressMsg(k)); return cmd }
func contentText(m *Model, s string) tea.Cmd {
	return contentKey(m, tea.Key{Code: []rune(s)[0], Text: s})
}

func TestContentEntrypointsAndCapabilities(t *testing.T) {
	m, _ := contentFixture()
	_, _ = m.openSongMenu(hitZone{target: hitLibrarySong, index: 1}, 10, 10)
	if !menuHasAct(m.menu, actComments) || !menuHasAct(m.menu, actRelated) {
		t.Fatal("missing menu entries")
	}
	_, cmd := m.runMenuAction(*m.menu, menuItem{act: actComments})
	m.menu = nil
	if cmd == nil || !m.content.busy || m.content.song.ID != m.songs[1].ID || !strings.Contains(m.View().Content, "加载中") {
		t.Fatal("comments did not open")
	}
	m.Update(cmd())
	if !strings.Contains(ansi.Strip(m.View().Content), "BV2 第 1 页的评论") {
		t.Fatal("menu target mismatch")
	}
	contentKey(m, tea.Key{Code: tea.KeyEscape})
	m.view = viewLyrics
	cmd = contentText(m, "i")
	m.Update(cmd())
	if m.content.song.ID != m.current.ID {
		t.Fatal("lyrics should use current song")
	}
	cmd = contentKey(m, tea.Key{Code: tea.KeyTab})
	m.Update(cmd())
	if m.content.kind != contentRelated || len(m.content.related) != 1 {
		t.Fatal("tab should load related")
	}
	m.closeContent()
	m.view = viewLibrary
	cmd = contentText(m, "I")
	if cmd == nil || m.content.kind != contentRelated {
		t.Fatal("related keyboard entry")
	}
	m.closeContent()
	m.sources = source.NewRegistry(&fakeSource{id: source.Bilibili})
	_, _ = m.openSongMenu(hitZone{target: hitLibrarySong, index: 0}, 10, 10)
	if menuHasAct(m.menu, actComments) || menuHasAct(m.menu, actRelated) {
		t.Fatal("unsupported capability shown")
	}
	m.menu = nil
	if cmd := m.openComments(m.songs[0]); cmd != nil || !strings.Contains(m.content.err, "不支持") {
		t.Fatal("unsupported should be explicit")
	}
	m.sources = nil
	if cmd := m.openRelated(m.songs[0]); cmd != nil || !strings.Contains(m.content.err, "不支持") {
		t.Fatal("nil registry should be safe")
	}
}

func TestContentPaginationRetryAndStaleResponses(t *testing.T) {
	m, s := contentFixture()
	first := m.openComments(m.songs[0])
	oldFirst := first().(commentsMsg)
	m.Update(oldFirst)
	next := contentText(m, "]")
	if next == nil || !m.content.busy {
		t.Fatal("next page not requested")
	}
	oldSecond := next().(commentsMsg)
	m.Update(oldSecond)
	if m.content.page != 2 || len(m.content.comments) != 1 || m.content.comments[0].ID != "2" || m.content.hasMore {
		t.Fatalf("pagination: %+v", m.content)
	}
	if cmd := contentText(m, "]"); cmd != nil {
		t.Fatal("last page must stop")
	}
	m.Update(contentText(m, "[")())
	if m.content.page != 1 {
		t.Fatal("previous page")
	}
	// 重开同一首时，曾经的翻页代号也不能碰撞。
	fresh := m.openComments(m.songs[0])
	gen := m.content.request
	m.Update(oldSecond)
	m.Update(oldFirst)
	if !m.content.busy || m.content.request != gen || len(m.content.comments) != 0 {
		t.Fatal("stale result replaced reopened panel")
	}
	m.Update(fresh())
	// 第 2 页失败保留第 1 页，r 仍重试第 2 页。
	original := s.comments
	s.comments = func(context.Context, source.Ref, int) (source.CommentPage, error) {
		return source.CommentPage{}, errors.New("接口暂时不可用")
	}
	m.Update(contentText(m, "]")())
	if m.content.busy || m.content.page != 1 || m.content.err == "" || len(m.content.comments) != 1 {
		t.Fatal("page failure lost existing result")
	}
	s.comments = original
	m.Update(contentText(m, "r")())
	if m.content.page != 2 || m.content.err != "" {
		t.Fatal("retry did not load requested page")
	}
	m.closeContent()
	m.Update(oldSecond)
	if m.content != nil {
		t.Fatal("late response reopened panel")
	}
}

func TestContentCancelEmptyAndError(t *testing.T) {
	m, s := contentFixture()
	s.comments = func(ctx context.Context, _ source.Ref, _ int) (source.CommentPage, error) {
		return source.CommentPage{}, ctx.Err()
	}
	cmd := m.openComments(m.songs[0])
	m.closeContent()
	if msg := cmd().(commentsMsg); !errors.Is(msg.err, context.Canceled) {
		t.Fatal("closing must cancel request")
	}
	s.comments = func(context.Context, source.Ref, int) (source.CommentPage, error) { return source.CommentPage{}, nil }
	m.Update(m.openComments(m.songs[0])())
	if !strings.Contains(m.View().Content, "暂无评论") {
		t.Fatal("empty comments")
	}
	s.related = func(context.Context, source.Ref) ([]source.Candidate, error) { return nil, nil }
	m.Update(m.openRelated(m.songs[0])())
	if !strings.Contains(m.View().Content, "暂无相关推荐") {
		t.Fatal("empty related")
	}
	s.related = func(context.Context, source.Ref) ([]source.Candidate, error) {
		return nil, errors.New("风控，请稍后再试")
	}
	current, queue, position := *m.current, len(m.queue), m.position
	m.Update(m.openRelated(m.songs[0])())
	if !strings.Contains(m.View().Content, "风控") || *m.current != current || len(m.queue) != queue || m.position != position {
		t.Fatal("content failure changed playback")
	}
}

func TestContentRenderScrollAndMouse(t *testing.T) {
	m, _ := contentFixture()
	m.openComments(m.songs[0])
	m.content.busy = false
	m.content.comments = []source.Comment{{Author: "读者\n\x1b[31m", Content: strings.Repeat("长评论内容，保留全部文字。", 100) + "\n最后一行\x1b]0;bad title\a", CreatedAt: time.Unix(1700000000, 0)}}
	for _, size := range [][2]int{{100, 30}, {60, 18}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		m.content.cursor = 0
		assertFrame(t, m.View().Content, m.width, m.height)
		contentKey(m, tea.Key{Code: tea.KeyEnd})
		frame := m.View().Content
		assertFrame(t, frame, m.width, m.height)
		if !strings.Contains(ansi.Strip(frame), "最后一行") || strings.Contains(frame, "bad title") {
			t.Fatal("long comment cannot be read safely to end")
		}
	}
	m.content.cursor = 0
	_, _ = m.handleMouseWheel(tea.Mouse{Button: tea.MouseWheelDown})
	if m.content.cursor != 3 {
		t.Fatal("wheel didn't scroll comments")
	}
	m.content.kind = contentRelated
	for i := 0; i < 30; i++ {
		m.content.related = append(m.content.related, source.Candidate{Track: source.Track{Title: fmt.Sprintf("推荐 %02d", i), Artist: "歌手"}})
	}
	contentKey(m, tea.Key{Code: tea.KeyEnd})
	assertFrame(t, m.View().Content, m.width, m.height)
	if !strings.Contains(m.View().Content, "推荐 29") {
		t.Fatal("last related item hidden")
	}
	// 点击弹层外只关闭弹层，不触发底层歌曲。
	current := *m.current
	_, _ = m.handleMouseClick(tea.Mouse{Button: tea.MouseLeft, X: 0, Y: 0})
	if m.content != nil || *m.current != current {
		t.Fatal("overlay click leaked")
	}
}

func TestContentRelatedActions(t *testing.T) {
	m, _ := contentFixture()
	m.Update(m.openRelated(m.songs[0])())
	n := len(m.queue)
	contentText(m, "E")
	if len(m.queue) != n+1 || m.queue[len(m.queue)-1].RemoteID != "BVrelated" || m.content == nil || !strings.Contains(m.content.feedback, "已加入队列") {
		t.Fatal("related enqueue failed")
	}
	cmd := contentKey(m, tea.Key{Code: tea.KeyEnter})
	if cmd == nil || m.content != nil || m.queue[m.queueIdx].ExternalID() != "BVrelated" {
		t.Fatal("related play not dispatched with queue index")
	}
}

func TestContentSelectionUsesSearchAndQueue(t *testing.T) {
	m, _ := contentFixture()
	for _, v := range []view{viewQueue, viewSearch} {
		m.view = v
		m.queueCursor = 1
		m.searchMode = searchSource
		m.searchResults = []source.Candidate{{Track: source.Track{Source: source.Bilibili, ExternalID: "BVsearch", Title: "搜索歌曲"}}}
		cmd := contentText(m, "i")
		if cmd == nil {
			t.Fatal("no command")
		}
		m.Update(cmd())
		want := "BV2"
		if v == viewSearch {
			want = "BVsearch"
		}
		if m.content.song.ExternalID() != want {
			t.Fatalf("wrong selected song: %+v", m.content.song)
		}
		m.closeContent()
	}
	m.view = viewLibrary
	m.songs = nil
	if cmd := contentText(m, "i"); cmd != nil || m.content != nil {
		t.Fatal("empty selection")
	}
	// 未知音源不会按平台名兜底。
	if cmd := m.openComments(store.Song{Source: "unknown", RemoteID: "1"}); cmd != nil || m.content.err == "" {
		t.Fatal("unknown source")
	}
}

func TestContentDemoFrames(t *testing.T) {
	for _, name := range []string{"comments", "related"} {
		for _, size := range [][2]int{{100, 30}, {60, 18}, {40, 12}} {
			frame := RenderDemo(name, size[0], size[1])
			assertFrame(t, frame, size[0], size[1])
			want := "评论区"
			if name == "related" {
				want = "相关推荐"
			}
			if !strings.Contains(frame, want) {
				t.Fatalf("demo %s missing content", name)
			}
		}
	}
}
