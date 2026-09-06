---
headline: Closing a dialog no longer leaves its border on screen
---
- **Closing a dialog forces a full repaint.** A dialog is a centred box and is
  almost never the width of the frame that replaces it — the pane-setup step is
  at least 70 columns, the split step 60, the processes list 92. Bubble Tea's
  cell diff left the box's border columns standing on rows the new frame painted
  identically, and the notification sidebar is where that showed up, because it
  is not drawn at all while a dialog is open. Two exits already forced the
  repaint and the one that actually closes the create-pane flow did not; the
  guarantee now lives at the single point that sees a dialog close, so a new
  dialog cannot miss it.
