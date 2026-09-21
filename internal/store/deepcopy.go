package store

import "encoding/json"

func deepCopy(st *State) *State {
	b, err := json.Marshal(st)
	if err != nil {
		panic(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return &out
}
