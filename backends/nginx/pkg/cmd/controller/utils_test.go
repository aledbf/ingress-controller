/*
Copyright 2015 The Kubernetes Authors.

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

package main

import "testing"

func TestDiff(t *testing.T) {
	tests := []struct {
		a   []byte
		b   []byte
		len int
	}{
		{[]byte(""), []byte(""), 0},
		{[]byte("a"), []byte("a"), 0},
		{[]byte("a"), []byte("b"), 274},
	}

	for _, test := range tests {
		b, err := diff(test.a, test.b)
		if err != nil {
			t.Fatalf("unexpected error returned: %v", err)
		}
		if len(b) != test.len {
			t.Fatalf("expected %v but %v returned", test.len, len(b))
		}
	}
}
