# Retention and gc: remove the old savepoints, then reclaim their space.

Source "common.tape"
Output "gc.mp4"


Set TypingSpeed 45ms
Type "arcave savepoint list"
Enter
Wait+Screen /wednesday/
Wait
Sleep 2.5s

Type "arcave savepoint rm --retain-recent 1"
Enter
Wait+Screen /Removed/
Wait
Sleep 3s

Hide
Type "clear"
Enter
Wait
Show

Type "# the records are gone; the bytes stay until gc"
Enter
Type "arcave gc --dry-run"
Enter
Wait+Screen /Garbage found/
Wait
Sleep 4s

Hide
Type "clear"
Enter
Wait
Show

Type "arcave gc"
Enter
Wait+Screen /Garbage collected/
Wait
Sleep 5s
