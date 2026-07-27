package model

// DefaultSession is the session segment every store is rooted under today. A
// session owns everything that dies with it, and there is exactly one, so
// nothing selects it yet — the segment is laid down now because introducing it
// later would move every existing key.
const DefaultSession = "global"
