# tools generate-compose: a compose file rebuilt from the running project.

Source "common.tape"
Output "generate-compose.mp4"

Set TypingSpeed 45ms
Type "arcave tools generate-compose -o recovered.yaml"
Enter
Wait
Sleep 2.5s

Hide
Type "clear"
Enter
Wait
Show

Type "head -20 recovered.yaml"
Enter
Wait
Sleep 5s
