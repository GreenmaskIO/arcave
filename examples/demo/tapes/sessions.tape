# Sessions: fork the baseline, break the copy, come back to the untouched baseline.

Source "common.tape"
Output "sessions.mp4"

Set PlaybackSpeed 1.4

Set TypingSpeed 45ms
Type "arcave session create --name feature-x --from baseline"
Enter
Wait+Screen /Session feature-x/
Wait
Sleep 2s
Type "arcave session set feature-x"
Enter
Wait
Sleep 2s

Hide
Type "clear"
Enter
Wait
Show

Type "# work on the feature: the data changes, the savepoint goes to feature-x"
Enter
Set TypingSpeed 15ms
Type "docker compose exec postgres psql -U postgres demo -c 'drop table notes'"
Enter
Wait
Set TypingSpeed 45ms
Type "arcave sp create broken"
Enter
Wait+Screen /roll back:/
Wait
Sleep 3s

Hide
Type "clear"
Enter
Wait
Show

Type "arcave session list"
Enter
Wait+Screen /feature-x/
Wait
Sleep 3s

Hide
Type "clear"
Enter
Wait
Show

Type "arcave session unset"
Enter
Wait
Type "arcave rollback seed"
Enter
Wait+Screen /Rolled back to/
Wait
Sleep 3s

Hide
Type "clear"
Enter
Wait
Show

Set TypingSpeed 15ms
Type "docker compose exec postgres psql -U postgres demo -c 'select id, text from notes'"
Enter
Wait
Sleep 4s
