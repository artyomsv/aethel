---
headline: Worktree picker: type to search, recent work first
---
- **The setup dialog's Worktree field can be searched.** Focus the field and type;
  the list narrows to the worktrees whose branch *or folder* contains the text,
  case-insensitively. Backspace edits, Esc clears (a second Esc backs out of the
  dialog as before), Enter picks the highlighted match and shows the whole list again
  with the choice marked. `j`/`k` are letters now — Up/Down still move.
- **Worktrees are ordered by recent work, not by folder name.** The worktree the pane
  you are splitting sits in comes first, tagged `(current)`, then the rest by their
  branch's last commit, newest first. git's own order is alphabetical by folder, which
  put the checkout being worked on today at row 49 of 58 in a six-row window. An older
  daemon that sends no dates keeps git's order.
- **Long rows keep the folder name.** A path wider than the dialog is cut at its head
  (`…/monorepo-worktrees/fix-verification-service`) rather than its tail, so the part
  you recognise from the pane header survives. Before, a row whose branch shared no
  word with its folder read as a worktree that was not there.
