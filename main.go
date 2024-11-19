package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/effects"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
)

var supportedFormats = []string{"mp3"}

// sample rate for mp3
const basicSampleRate beep.SampleRate = 44100
const executableName = "gomusic"
const helpString = "Usage: " + executableName + " [DIRECTORY]"

// program pointer to send messages from other threads
var program *tea.Program

// Music track
type track struct {
	// path to track
	path string
	// stream struct
	stream beep.StreamSeekCloser
	// resampled track (to avoid bugs with playback speed)
	resampled beep.Streamer
	// track format
	format beep.Format
	ended  bool
}

var (
	errFormatUnsupported = errors.New("format unsupported")
	errFileIsNotTrack    = errors.New("file is not a track")
)

func loadTrack(trackPath string) (track, error) {
	fileFormat := filepath.Ext(trackPath)
	if fileFormat != "" {
		fileFormat = fileFormat[1:]
	}
	if !slices.Contains(supportedFormats, fileFormat) {
		return track{}, errFormatUnsupported
	}
	s := track{}
	s.path = trackPath
	f, err := os.Open(trackPath)
	if err != nil {
		return track{}, err
	}
	fileStat, err := f.Stat()
	if err != nil {
		return track{}, err
	}
	if fileStat.IsDir() {
		return track{}, errFileIsNotTrack
	}

	// currently supports mp3 only
	streamer, format, err := mp3.Decode(f)
	s.stream = streamer
	s.format = format
	s.resampled = beep.Resample(4, s.format.SampleRate, basicSampleRate, s.stream)
	if err != nil {
		return track{}, err
	}
	return s, nil
}

type tracksQueue struct {
	queue []track
	// stream controller. allows to pause and resume tracks
	ctrl *beep.Ctrl
	// stream volume. allows to control volume
	volume effects.Volume
	// current track index in queue
	currentTrack       int
	speakerInitialized bool
	// change of the volume in percents, for example 100 means current volume is 200%
	volumeChange int
}

func newTrackQueue() *tracksQueue {
	queue := tracksQueue{
		queue: make([]track, 0),
		ctrl:  &beep.Ctrl{},
	}
	queue.volume = effects.Volume{
		// see https://github.com/gopxl/beep/wiki/Hello,-Beep!
		Streamer: queue.ctrl,
		Base:     10,
		Volume:   0,
		Silent:   false,
	}
	return &queue
}

func (s *tracksQueue) getTracks() []track {
	return s.queue
}

func (s *tracksQueue) hasTrack(trackPath string) bool {
	for _, track := range s.queue {
		if track.path == trackPath {
			return true
		}
	}
	return false
}

func (s *tracksQueue) getCurrentTrack() (track, bool) {
	if s.len() == 0 {
		return track{}, false
	}
	return s.queue[s.currentTrack], true
}

func (s *tracksQueue) getCurrentTrackIndex() int {
	return s.currentTrack
}

func (s *tracksQueue) addTrack(track track) {
	s.queue = append(s.queue, track)
	s.rebuildStreamer()
}

// rebuilds stream sequence
func (s *tracksQueue) rebuildStreamer() {
	streamers := make([]beep.Streamer, 0)
	for _, track := range s.queue {
		if !track.ended {
			seq := beep.Seq(track.resampled, beep.Callback(func() {
				program.Send("f")
			}))
			streamers = append(streamers, seq)
		}
	}
	stream := beep.Seq(streamers...)
	speaker.Lock()
	s.ctrl.Streamer = stream
	speaker.Unlock()
}

func (s *tracksQueue) nextTrack() {
	if s.len() != 0 {
		s.queue[s.currentTrack].ended = true
	}
	if s.currentTrack+1 >= s.len() {
		return
	}
	s.currentTrack += 1
	s.rebuildStreamer()
	speaker.Clear()
	s.play()
}

func (s *tracksQueue) prevTrack() {
	if s.currentTrack-1 < 0 {
		return
	}
	s.currentTrack -= 1
	s.queue[s.currentTrack].ended = false
	s.rebuildStreamer()
	speaker.Clear()
	s.play()
}

func (s *tracksQueue) restartCurrentTrack() {
	if s.len() == 0 {
		return
	}
	s.restartTrack(s.currentTrack)
}

func (s *tracksQueue) restartTrack(index int) {
	currentSong := &s.queue[index]
	currentSong.ended = false
	ended := currentSong.stream.Position() == currentSong.stream.Len()
	currentSong.stream.Seek(0)
	if ended {
		speaker.Lock()
		currentSong.resampled = beep.Resample(4, basicSampleRate, currentSong.format.SampleRate, currentSong.stream)
		speaker.Unlock()
		s.rebuildStreamer()
		s.play()
	}
}

func (s *tracksQueue) restartQueue() {
	for i := range s.queue {
		s.restartTrack(i)
	}
	s.currentTrack = 0
	s.rebuildStreamer()
}

func (s *tracksQueue) play() {
	speaker.Clear()
	if s.len() != 0 {
		speaker.Play(&s.volume)
	}
}

func (s *tracksQueue) changeVolume(percents int) {
	if s.volumeChange+percents < -100 {
		return
	}
	s.volumeChange += percents
	if s.volumeChange == -100 {
		s.volume.Silent = true
	} else {
		s.volume.Silent = false
	}
	// s.volume.Volume is just a power of base number
	// that's how we calculate needed volume for our percent
	s.volume.Volume = math.Log10(100+float64(s.volumeChange)) - 2
	speaker.Clear()
	speaker.Play(&s.volume)
}

func (s tracksQueue) getVolumePercents() int {
	return int(math.Round(100 * math.Pow(s.volume.Base, s.volume.Volume)))
}

func (s *tracksQueue) unpause() {
	speaker.Lock()
	s.ctrl.Paused = false
	speaker.Unlock()
}

func (s *tracksQueue) len() int {
	return len(s.queue)
}

func (s *tracksQueue) pause() {
	speaker.Lock()
	s.ctrl.Paused = true
	speaker.Unlock()
}

func (s *tracksQueue) paused() bool {
	return s.ctrl.Paused
}

func (s *tracksQueue) removeTrack(trackName string) {
	trackIndex := -1
	prev := 0
	for i, track := range s.queue {
		if filepath.Base(track.path) == trackName {
			trackIndex = i
			break
		}
		prev = i
	}
	if trackIndex == -1 {
		return
	}
	s.currentTrack = prev
	s.queue = slices.Delete(s.queue, trackIndex, trackIndex+1)
}

// releases all resources and cleans queue
func (s *tracksQueue) clear() {
	for _, track := range s.queue {
		track.stream.Close()
	}
	speaker.Lock()
	s.ctrl.Streamer = nil
	speaker.Unlock()
	s.currentTrack = 0
	s.queue = make([]track, 0)
}

type appState struct {
	cursor      int
	currentDir  string
	choices     []string
	tracksQueue tracksQueue
	showHelp    bool
}

func (a appState) Init() tea.Cmd {
	// Just return `nil`, which means "no I/O right now, please."
	return nil
}

func (a appState) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case string:
		a.tracksQueue.nextTrack()
	// Is it a key press?
	case tea.KeyMsg:

		// Cool, what was the actual key pressed?
		switch msg.String() {

		// These keys should exit the program.
		case "ctrl+c", "q":
			a.releaseResources()
			return a, tea.Quit

		// The "up" and "k" keys move the cursor up
		case "up", "k":
			if a.cursor > 0 {
				a.cursor--
			}
		case "r":
			a.tracksQueue.restartCurrentTrack()
		case "R":
			a.tracksQueue.restartQueue()
		case "d":
			if len(a.choices) != 0 {
				a.tracksQueue.removeTrack(a.choices[a.cursor])
			}
		// The "down" and "j" keys move the cursor down
		case "down", "j":
			if a.cursor < len(a.choices)-1 {
				a.cursor++
			}
		case "f":
			a.tracksQueue.nextTrack()
		case "F":
			a.tracksQueue.prevTrack()
		case "-":
			a = a.goUpDir().updateChoices()
		// The "enter" key and the spacebar (a literal space) toggle
		// the selected state for the item that the cursor is pointing at.
		case " ":
			if len(a.choices) == 0 {
				break
			}
			trackPath := filepath.Join(a.currentDir, a.choices[a.cursor])
			if a.tracksQueue.hasTrack(trackPath) {
				break
			}
			track, err := loadTrack(trackPath)
			if errors.Is(errFormatUnsupported, err) {
				break
			}
			if errors.Is(errFileIsNotTrack, err) {
				break
			}
			if err != nil {
				a.releaseResources()
				log.Println(err)
				return a, tea.Quit
			}
			a.tracksQueue.addTrack(track)
			a.tracksQueue.play()
			if a.cursor+1 < len(a.choices) {
				a.cursor++
			}
		case "c":
			a.tracksQueue.clear()
		case "p":
			if len(a.choices) == 0 {
				break
			}
			if a.tracksQueue.paused() {
				a.tracksQueue.unpause()
			} else {
				a.tracksQueue.pause()
			}
		case "]":
			a.tracksQueue.changeVolume(10)
		case "[":
			a.tracksQueue.changeVolume(-10)
		case "?":
			a.showHelp = !a.showHelp
		case "enter":
			a = a.goToCursorDir().updateChoices()
		}

	}

	// Return the updated model to the Bubble Tea runtime for processing.
	// Note that we're not returning a command.
	return a, nil
}

func (a appState) View() string {
	if a.showHelp {
		// The header
		s := fmt.Sprint("controls:\n\n")
		s += "(k) or (arrow up) up\n"
		s += "(j) or (arrow down) down\n"
		s += "(f) next track\n"
		s += "(F) previous track\n"
		s += "([) volume down\n"
		s += "(]) volume up\n"
		s += "(p) pause/unpause\n"
		s += "(c) clear track queue\n"
		s += "(r) restart current track\n"
		s += "(R) restart queue\n"
		s += "(<Space>) add track to queue\n"
		s += "(d) remove track from queue\n"
		s += "(<Enter>) enter directory\n"
		s += "(-) directory up\n"
		s += "\nPress q to quit, ? to toggle help\n"
		return s
	}
	// The header
	s := fmt.Sprintf("volume: %d", a.tracksQueue.getVolumePercents())
	currentTrack, ok := a.tracksQueue.getCurrentTrack()
	if ok {
		s = fmt.Sprintf("%s, playing: %s\n \n", s, filepath.Base(currentTrack.path))
	} else {
		s += "\n \n"
	}

	// Iterate over our choices
	choicesWindowSize := 16
	choicesWindowStart := 0
	choicesWindowEnd := len(a.choices)
	if a.cursor-choicesWindowSize/2 > choicesWindowStart {
		choicesWindowStart = a.cursor - choicesWindowSize/2
	}
	if a.cursor+choicesWindowSize/2 < choicesWindowEnd {
		choicesWindowEnd = a.cursor + choicesWindowSize/2
	}
	for i := choicesWindowStart; i < choicesWindowEnd; i++ {

		// Is the cursor pointing at this choice?
		cursor := " " // no cursor
		if a.cursor == i {
			cursor = ">" // cursor!
		}

		// Is this choice selected?
		checked := " " // not selected
		for j, track := range a.tracksQueue.getTracks() {
			if filepath.Base(track.path) != a.choices[i] {
				continue
			}
			if j == a.tracksQueue.getCurrentTrackIndex() {
				checked = "*"
			} else {
				checked = ">"
			}
			break
		}

		// Render the row
		s += fmt.Sprintf("%s [%s] %s\n", cursor, checked, a.choices[i])
	}

	// The footer
	s += "\nPress q to quit, ? to toggle help\n"

	// Send the UI for rendering
	return s
}

func (a appState) releaseResources() {
	a.tracksQueue.clear()
}

func (a appState) exitError(err error) {
	panic(err)
}

func (a appState) goUpDir() appState {
	newDir := filepath.Dir(a.currentDir)
	if newDir != a.currentDir {
		a.cursor = 0
	}
	a.currentDir = newDir
	return a
}

func (a appState) goToCursorDir() appState {
	if len(a.choices) == 0 {
		return a
	}
	currentChoice := a.choices[a.cursor]
	newDir := filepath.Join(a.currentDir, currentChoice)
	file, err := os.Open(newDir)
	if err != nil {
		a.exitError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		a.exitError(err)
	}
	if info.IsDir() {
		a.currentDir = newDir
		a.cursor = 0
	}
	return a
}

func (a appState) updateChoices() appState {
	files, err := os.ReadDir(a.currentDir)
	if err != nil {
		a.exitError(err)
	}
	choices := make([]string, len(files))
	for i, file := range files {
		choices[i] = file.Name()
	}
	a.choices = choices
	return a
}

func main() {
	speaker.Init(basicSampleRate, basicSampleRate.N(time.Second/10))
	var directoryPath string
	if len(os.Args) == 1 {
		curDir, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		directoryPath = curDir
	} else {
		var err error
		directoryPath, err = filepath.Abs(os.Args[1])
		if err != nil {
			log.Fatal("failed to resolve absolute path", err)
		}
	}
	if slices.Contains(os.Args, "--help") {
		fmt.Println(helpString)
		os.Exit(0)
	}
	program = tea.NewProgram(appState{
		cursor:      0,
		currentDir:  directoryPath,
		choices:     []string{},
		tracksQueue: *newTrackQueue(),
	}.updateChoices())
	if _, err := program.Run(); err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
}
