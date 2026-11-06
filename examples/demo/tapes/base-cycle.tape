# The base cycle: take a savepoint, break the data, roll back.

Source "common.tape"
Output "base-cycle.gif"
Output "base-cycle.mp4"

# The waits are real; played back faster so the loop stays short.
Set PlaybackSpeed 1.4

Set TypingSpeed 15ms
Type "docker compose exec postgres psql -U postgres demo -c 'select id, text from notes'"
Enter
Wait
Sleep 1.5s

Set TypingSpeed 45ms
Type "arcave savepoint create before-migration"
Enter
Wait+Screen /roll back:/
Wait
Sleep 5s

Hide
Type "clear"
Enter
Wait
Show

Set TypingSpeed 45ms
Type "# a migration goes wrong"
Enter
Set TypingSpeed 15ms
Type "docker compose exec postgres psql -U postgres demo -c 'drop table notes'"
Enter
Wait
Set TypingSpeed 15ms
Type "docker compose exec postgres psql -U postgres demo -c 'select id, text from notes'"
Enter
Wait
Sleep 1.5s

Set TypingSpeed 45ms
Type "arcave rollback before-migration"
Enter
Wait+Screen /Rolled back to/
Wait
Sleep 5s

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
