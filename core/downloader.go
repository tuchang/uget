package core

import (
	log "github.com/Sirupsen/logrus"
	"github.com/chuckpreslar/emission"
	"github.com/eapache/channels"
	"github.com/uget/uget/core/action"
	"net/http"
	"net/http/cookiejar"
)

type Downloader struct {
	*emission.Emitter
	Queue        *Queue
	Client       *http.Client
	MaxDownloads int
	dlChannel    chan *Download
	dlBuffer     *channels.RingChannel
	done         chan struct{}
}

const (
	eDownload = iota
)

func NewDownloader() *Downloader {
	jar, _ := cookiejar.New(nil)
	dl := &Downloader{
		Emitter:      emission.NewEmitter(),
		Queue:        NewQueue(),
		Client:       &http.Client{Jar: jar},
		MaxDownloads: 3,
		dlChannel:    make(chan *Download),
		dlBuffer:     channels.NewRingChannel(5),
		done:         make(chan struct{}, 1),
	}
	channels.Unwrap(dl.dlBuffer, dl.dlChannel)
	for _, p := range providers {
		TryLogin(p, dl)
	}
	return dl
}

func (d *Downloader) Start(async bool) {
	if async {
		d.StartAsync()
	} else {
		d.StartSync()
	}
}

func (d *Downloader) StartSync() {
	d.StartAsync()
	<-d.done
}

func (d *Downloader) Finished() <-chan struct{} {
	return d.done
}

func (d *Downloader) work() {
	for f := range d.Queue.Pop() {
		d.Download(f)
		d.Queue.Done()
	}
}

func (d *Downloader) StartAsync() {
	for i := 0; i < d.MaxDownloads; i++ {
		go d.work()
	}
	go func() {
		d.Queue.Wait()
		d.Queue.Close()
		close(d.done)
	}()
}

func (d *Downloader) Download(fs *FileSpec) {
	log.Debugf("Downloading remote file: %v", fs.URL)
	req, _ := http.NewRequest("GET", fs.URL.String(), nil)
	resp, err := d.Client.Do(req)
	if err != nil {
		log.Errorf("Error while requesting %v: %v", fs.URL.String(), err)
		return
	}
	// Reverse iterate -> last provider is the default provider
	FindProvider(func(p Provider) bool {
		a := p.Action(resp, d)
		switch a.Value {
		case action.NEXT:
			return false
		case action.REDIRECT:
			fs2 := &FileSpec{}
			*fs2 = *fs // Copy fs to fs2
			fs2.URL = resp.Request.URL.ResolveReference(a.RedirectTo)
			log.Debugf("Got redirect instruction from %v provider. Location: %v", p.Name(), fs2.URL)
			d.Download(fs2)
		case action.GOAL:
			download := NewDownloadFromResponse(resp)
			d.Emit(eDownload, download)
			download.Start()
		case action.BUNDLE:
			log.Debugf("Got bundle instructions from %v provider. Bundle size: %v", p.Name(), len(a.Links))
			d.Queue.AddLinks(a.Links, fs.Priority)
		case action.DEADEND:
			log.Debugf("Reached deadend (via %v provider).", p.Name())
		}
		return true
	})
}

func (d *Downloader) OnDownload(f func(*Download)) {
	d.On(eDownload, f)
}
