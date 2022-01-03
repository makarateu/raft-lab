package common

import (
	"github.com/golang/glog"
)

// DieIfError will log e fatally if it is not nil.
func DieIfError(e error) {
	if e != nil {
		glog.Fatalln("Error: ", e)
	}
}
