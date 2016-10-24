/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package watch

import (
	"log"
	"path"

	"gopkg.in/fsnotify.v1"
)

// FileWatcher defines a watch over a file
type FileWatcher struct {
	file    string
	watcher *fsnotify.Watcher
	// onEvent callback to be invoked after the file being watched changes
	onEvent func()
}

// NewFileWatcher creates a new FileWatcher
func NewFileWatcher(file string, onEvent func()) (FileWatcher, error) {
	fw := FileWatcher{
		file:    file,
		onEvent: onEvent,
	}
	err := fw.watch()
	return fw, err
}

// Close ends the watch
func (f FileWatcher) Close() error {
	return f.watcher.Close()
}

// watch creates a fsnotify watcher for a file and create of write events
func (f *FileWatcher) watch() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	f.watcher = watcher

	dir, file := path.Split(f.file)
	go func(file string) {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Write == fsnotify.Write ||
					event.Op&fsnotify.Create == fsnotify.Create &&
						event.Name == file {
					f.onEvent()
				}
			case err := <-watcher.Errors:
				if err != nil {
					log.Printf("error watching file: %v\n", err)
				}
			}
		}
	}(file)
	return watcher.Add(dir)
}
