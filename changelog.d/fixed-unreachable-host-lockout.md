---
headline: A host that stays offline no longer freezes the whole client
---
- **A remote host that goes away no longer locks up your keyboard.** While a link is
  reconnecting, Quil drops input so a keystroke cannot arrive late in an agent
  session that has moved on. That is right for a blip and wrong for a machine
  somebody switched off: `ssh` reports a powered-off host as a connection timeout,
  which is a *transient* failure, so the retry loop climbed forever and every key
  except quit was swallowed for as long as it climbed — with the dead host's project
  on screen, even the key that switches to another project.

  The freeze is now bounded. After 45 seconds of an outage — or the moment the retry
  loop parks itself on a failure that cannot heal, such as a rejected key — the
  client hands the keyboard back. The banner stays up and the retry loop keeps
  running, so a host that comes back still reattaches on its own; it just stops
  being modal. Nothing is queued across an outage, so a key pressed at an
  unreachable daemon still goes nowhere rather than arriving late.
