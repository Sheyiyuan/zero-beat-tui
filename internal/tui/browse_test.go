package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"github.com/sheyiyuan/zero-beat-core/app"
	"github.com/sheyiyuan/zero-beat-core/source"
	"github.com/sheyiyuan/zero-beat-core/store"
	"github.com/sheyiyuan/zero-beat-tui/internal/keymap"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type browseSourceFake struct {
	*contentSource
	browseFn   func(context.Context, source.BrowseRef, int) (source.BrowsePage, error)
	profilesFn func(context.Context, source.Ref) ([]source.BrowseEntry, error)
}

func (s *browseSourceFake) Caps() source.Capability { return s.contentSource.Caps() | source.CapBrowse }
func (s *browseSourceFake) BrowseRoots() []source.BrowseEntry {
	return []source.BrowseEntry{{Title: "测试电台", Ref: source.BrowseRef{Source: s.ID(), Kind: source.BrowseRadios}}}
}
func (s *browseSourceFake) TrackProfiles(ctx context.Context, r source.Ref) ([]source.BrowseEntry, error) {
	if s.profilesFn != nil {
		return s.profilesFn(ctx, r)
	}
	return []source.BrowseEntry{{Title: "歌手甲", Ref: source.BrowseRef{Source: r.Source, Kind: source.BrowseArtist, ID: "12"}}, {Title: "歌手乙", Ref: source.BrowseRef{Source: r.Source, Kind: source.BrowseArtist, ID: "13"}}}, nil
}
func (s *browseSourceFake) Browse(ctx context.Context, r source.BrowseRef, p int) (source.BrowsePage, error) {
	return s.browseFn(ctx, r, p)
}
func browseFixture() (*Model, *browseSourceFake) {
	m, cs := contentFixture()
	s := &browseSourceFake{contentSource: cs}
	m.sources = source.NewRegistry(s)
	s.browseFn = func(_ context.Context, r source.BrowseRef, p int) (source.BrowsePage, error) {
		t := source.Track{Source: s.ID(), ExternalID: fmt.Sprintf("track-%s-%d", r.ID, p), Page: 1, Title: fmt.Sprintf("第 %d 页作品", p)}
		return source.BrowsePage{Title: "主页", Description: "简介", Page: p, HasMore: p < 2, Entries: []source.BrowseEntry{{Title: t.Title, Track: &t}}}, nil
	}
	return m, s
}

func TestProfilesKeyUsesCurrentTrack(t *testing.T) {
	for _, view := range []view{viewLibrary, viewSearch, viewQueue, viewLyrics, viewDownloads} {
		t.Run(fmt.Sprint(view), func(t *testing.T) {
			m, _ := browseFixture()
			m.view = view
			m.libraryFocus, m.songIdx, m.queueCursor = focusSongs, 1, 1
			focused := m.songs[1]
			target := *m.current
			m.searchMode, m.searchEditing = searchSource, false
			m.searchResults = []source.Candidate{{Track: source.Track{Source: source.ID(focused.Source), ExternalID: focused.ExternalID(), Page: 1, Title: focused.Title}}}
			m.downloadItems = []downloadItem{{entry: store.DownloadEntry{Song: focused}}}
			cmd := contentText(m, "u")
			if cmd == nil || m.browse == nil || app.RefFromSong(*m.browse.song) != app.RefFromSong(target) {
				t.Fatalf("主页没有使用当前播放曲目: %+v", m.browse)
			}
			m.Update(cmd())
			m.Update(contentKey(m, tea.Key{Code: tea.KeyEnter})())
			if m.browse.result.Title != "主页" {
				t.Fatal("键盘无法进入歌手作品")
			}
			contentKey(m, tea.Key{Code: tea.KeyBackspace})
			if m.browse.song == nil {
				t.Fatal("键盘无法返回歌手列表")
			}
			contentKey(m, tea.Key{Code: tea.KeyEscape})
			if m.browse != nil {
				t.Fatal("键盘无法关闭主页")
			}
		})
	}
}

func TestProfilesKeyRemappingAcrossPanels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keymap.toml")
	if err := os.WriteFile(path, []byte("[keys]\nprofiles = [\"P\"]\ncomment_author = [\"O\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	km, err := keymap.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, panel := range []string{"queue", "browse", "comments", "related"} {
		t.Run(panel, func(t *testing.T) {
			m, s := browseFixture()
			m.keys, m.view, m.queueCursor = km, viewQueue, 1
			var parent *browseState
			switch panel {
			case "browse":
				m.Update(m.openBrowse(source.BrowseRef{Source: s.ID(), Kind: source.BrowseArtist, ID: "99"})())
				parent = m.browse
			case "comments":
				m.Update(m.openComments(m.songs[1])())
			case "related":
				m.Update(m.openRelated(m.songs[1])())
			}
			if cmd := contentText(m, "u"); cmd != nil {
				t.Fatal("旧主页键仍然生效")
			}
			m.Update(contentText(m, "P")())
			if m.content != nil || m.browse.song == nil || app.RefFromSong(*m.browse.song) != app.RefFromSong(*m.current) {
				t.Fatal("面板内改键未使用当前播放")
			}
			if parent != nil {
				contentKey(m, tea.Key{Code: tea.KeyBackspace})
				if m.browse != parent {
					t.Fatal("没有保留浏览返回路径")
				}
			}
		})
	}
	m, s := browseFixture()
	m.keys = km
	m.Update(m.openComments(m.songs[1])())
	m.content.comments[0].AuthorRef = source.BrowseRef{Source: s.ID(), Kind: source.BrowseUser, ID: "42"}
	if !strings.Contains(strings.Join(m.contentLines(), "\n"), "P 当前作者 · O 评论作者") {
		t.Fatal("提示未区分两个动作")
	}
	m.Update(contentText(m, "O")())
	if m.content != nil || m.browse.ref.ID != "42" {
		t.Fatal("独立评论作者入口失效")
	}
}

func TestCurrentProfilesSingleAuthorSkipsPicker(t *testing.T) {
	m, s := browseFixture()
	ref := source.BrowseRef{Source: s.ID(), Kind: source.BrowseArtist, ID: "12"}
	s.profilesFn = func(context.Context, source.Ref) ([]source.BrowseEntry, error) {
		return []source.BrowseEntry{
			{Title: "所属电台", Ref: source.BrowseRef{Source: s.ID(), Kind: source.BrowseRadio, ID: "8"}},
			{Title: "唯一作者", Ref: ref}, {Title: "同一作者", Ref: ref},
		}, nil
	}
	m.Update(m.openBrowse(source.BrowseRef{Source: s.ID(), Kind: source.BrowseRadios})())
	parent := m.browse
	lookup := contentText(m, "u")
	_, auto := m.Update(lookup())
	if auto == nil || m.browse.song != nil || m.browse.ref != ref || !m.browse.busy {
		t.Fatal("单作者没有自动进入主页")
	}
	if len(m.browse.history) != 1 || m.browse.history[0] != parent {
		t.Fatal("单作者留下了多余的选择层")
	}
	m.Update(auto())
	if m.browse.result.Title != "主页" {
		t.Fatal("没有加载主页")
	}
	contentKey(m, tea.Key{Code: tea.KeyBackspace})
	if m.browse != parent {
		t.Fatal("返回没有恢复原页面")
	}
}

func TestCurrentProfilesSingleAuthorFailureAndStaleResult(t *testing.T) {
	m, s := browseFixture()
	ref := source.BrowseRef{Source: s.ID(), Kind: source.BrowseUser, ID: "12"}
	s.profilesFn = func(context.Context, source.Ref) ([]source.BrowseEntry, error) {
		return []source.BrowseEntry{{Title: "UP 主", Ref: ref}}, nil
	}
	m.view = viewQueue
	lookup := contentText(m, "u")
	_, auto := m.Update(lookup())
	original := s.browseFn
	s.browseFn = func(context.Context, source.BrowseRef, int) (source.BrowsePage, error) {
		return source.BrowsePage{}, errors.New("主页暂时不可用")
	}
	m.Update(auto())
	if m.browse.err == "" || m.browse.ref != ref {
		t.Fatal("自动跳转失败后丢失目标")
	}
	s.browseFn = original
	m.Update(contentText(m, "r")())
	if m.browse.err != "" {
		t.Fatal("重试失败")
	}
	m.closeBrowse()
	lookup = contentText(m, "u")
	_, auto = m.Update(lookup())
	m.closeBrowse()
	fresh := m.openBrowse(source.BrowseRef{Source: s.ID(), Kind: source.BrowseUser, ID: "99"})
	m.Update(auto())
	if !m.browse.busy || m.browse.ref.ID != "99" {
		t.Fatal("迟到单作者主页覆盖了新页面")
	}
	m.Update(fresh())
}

func TestCurrentProfilesEmptyAbsentAndPlaybackSnapshot(t *testing.T) {
	m, s := browseFixture()
	m.current = nil
	m.view = viewQueue
	m.queueCursor = 1
	if contentText(m, "u") != nil || m.browse != nil || m.note.text != "当前没有正在播放的歌曲" {
		t.Fatal("无当前曲目时错误使用列表选择")
	}
	m.current = &m.songs[0]
	want := app.RefFromSong(*m.current)
	s.profilesFn = func(_ context.Context, r source.Ref) ([]source.BrowseEntry, error) {
		if r != want {
			t.Fatal("请求未保留按键时的曲目")
		}
		return nil, nil
	}
	cmd := contentText(m, "u")
	m.current = &m.songs[1]
	_, next := m.Update(cmd())
	if next != nil || m.browse.busy || len(m.browse.result.Entries) != 0 || !strings.Contains(m.View().Content, "未找到可用的作者主页") {
		t.Fatal("无作者时没有显示明确状态")
	}
}

func TestCurrentProfilesReturnsToInterruptedPage(t *testing.T) {
	m, s := browseFixture()
	old := m.openBrowse(source.BrowseRef{Source: s.ID(), Kind: source.BrowseArtist, ID: "99"})
	parent := m.browse
	m.Update(contentText(m, "u")())
	m.Update(old())
	if m.browse == parent {
		t.Fatal("父页迟到结果覆盖了作者列表")
	}
	resume := contentKey(m, tea.Key{Code: tea.KeyBackspace})
	if resume == nil || m.browse != parent {
		t.Fatal("返回没有恢复被取消的父页请求")
	}
	m.Update(resume())
	if m.browse.busy || m.browse.result.Title != "主页" {
		t.Fatal("父页仍停在加载中")
	}
}

func TestProfilesKeyDisableAndTextInput(t *testing.T) {
	m, _ := browseFixture()
	m.view, m.searchEditing = viewSearch, true
	contentText(m, "u")
	if m.browse != nil {
		t.Fatal("输入 u 打断了搜索")
	}
	path := filepath.Join(t.TempDir(), "keymap.toml")
	if err := os.WriteFile(path, []byte("[keys]\nprofiles = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	m.keys, err = keymap.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m.view = viewQueue
	if contentText(m, "u") != nil || m.browse != nil {
		t.Fatal("禁用主页键未生效")
	}
	m.Update(m.openComments(m.songs[0])())
	m.content.comments[0].AuthorRef = source.BrowseRef{Source: source.Bilibili, Kind: source.BrowseUser, ID: "42"}
	if contentText(m, "u") != nil || m.content == nil {
		t.Fatal("禁用后评论区仍响应主页键")
	}
}
func TestBrowseNavigationPaginationAndRetry(t *testing.T) {
	m, s := browseFixture()
	m.Update(m.openProfiles(m.songs[0])())
	if len(m.browse.result.Entries) != 2 {
		t.Fatal("multiple artists lost")
	}
	m.Update(contentKey(m, tea.Key{Code: tea.KeyEnter})())
	if len(m.browse.history) != 1 || m.browse.result.Title != "主页" {
		t.Fatal("enter failed")
	}
	original := s.browseFn
	s.browseFn = func(context.Context, source.BrowseRef, int) (source.BrowsePage, error) {
		return source.BrowsePage{}, errors.New("temporary failure")
	}
	m.Update(contentText(m, "]")())
	if m.browse.result.Page != 1 || m.browse.err == "" {
		t.Fatal("failed page discarded prior page")
	}
	s.browseFn = original
	m.Update(contentText(m, "r")())
	if m.browse.result.Page != 2 || m.browse.err != "" {
		t.Fatal("retry used wrong page")
	}
	if cmd := contentText(m, "]"); cmd != nil {
		t.Fatal("requested beyond last page")
	}
	m.Update(contentText(m, "[")())
	contentKey(m, tea.Key{Code: tea.KeyBackspace})
	if m.browse.song == nil || len(m.browse.result.Entries) != 2 {
		t.Fatal("back did not restore profiles")
	}
	contentKey(m, tea.Key{Code: tea.KeyEscape})
	if m.browse != nil {
		t.Fatal("escape")
	}
}
func TestBrowseStaleResponsesAndCancel(t *testing.T) {
	m, s := browseFixture()
	var captured context.Context
	original := s.browseFn
	s.browseFn = func(ctx context.Context, r source.BrowseRef, p int) (source.BrowsePage, error) {
		captured = ctx
		return original(ctx, r, p)
	}
	ref := source.BrowseRef{Source: s.ID(), Kind: source.BrowseArtist, ID: "12"}
	first := m.openBrowse(ref)
	msg := first()
	m.Update(msg)
	if captured.Err() != context.Canceled {
		t.Fatal("request context not released")
	}
	next := m.requestBrowse(2)
	m.closeBrowse()
	fresh := m.openBrowse(ref)
	m.Update(next())
	m.Update(msg)
	if !m.browse.busy {
		t.Fatal("stale response replaced reopened page")
	}
	m.Update(fresh())
	if m.browse.result.Page != 1 {
		t.Fatal("wrong fresh result")
	}
	m.closeBrowse()
	m.Update(msg)
	if m.browse != nil {
		t.Fatal("late response reopened browser")
	}
}
func TestBrowseQueueCommentsAndFrames(t *testing.T) {
	m, s := browseFixture()
	m.Update(m.openBrowse(source.BrowseRef{Source: s.ID(), Kind: source.BrowseArtist, ID: "12"})())
	before := len(m.queue)
	current := m.current.ID
	contentText(m, "E")
	if len(m.queue) != before+1 || m.current.ID != current || m.queue[len(m.queue)-1].RemoteID != "track-12-1" {
		t.Fatal("enqueue changed playback or lost ref")
	}
	m.Update(contentText(m, "i")())
	if m.content == nil || m.content.song.RemoteID != "track-12-1" {
		t.Fatal("wrong comments target")
	}
	contentKey(m, tea.Key{Code: tea.KeyEscape})
	if m.browse == nil || m.content != nil {
		t.Fatal("comments did not return to browser")
	}
	m.browse.result.Title = "主页\x1b[2J"
	m.browse.result.Description = "简介\n下一行\x00"
	m.browse.result.Entries[0].Title = strings.Repeat("很长的节目名字", 20)
	for _, size := range [][2]int{{100, 30}, {60, 18}, {40, 12}} {
		m.width, m.height = size[0], size[1]
		assertFrame(t, m.View().Content, m.width, m.height)
	}
	if m.textInputFocused() {
		t.Fatal("browser accepted type injection")
	}
	contentText(m, "a")
	if m.picker == nil || m.browse != nil {
		t.Fatal("playlist picker missing")
	}
}
func TestBrowsePaletteAndCommentAuthor(t *testing.T) {
	m, s := browseFixture()
	m.view = viewQueue
	contentText(m, "N")
	if m.palette == nil {
		t.Fatal("palette inaccessible from queue")
	}
	m.runPaletteAction(palBrowseLink)
	m.palette.input = "bilibili:user:12"
	cmd := m.commitPaletteName()
	m.Update(cmd())
	if m.browse.ref.Kind != source.BrowseUser {
		t.Fatal("link not opened")
	}
	m.closeBrowse()
	m.Update(m.openComments(m.songs[0])())
	m.content.comments[0].AuthorRef = source.BrowseRef{Source: s.ID(), Kind: source.BrowseUser, ID: "42"}
	m.Update(contentText(m, "O")())
	if m.content != nil || m.browse == nil || m.browse.ref.ID != "42" {
		t.Fatal("comment author target lost")
	}
}
