Feature: The completion menu closes with the draft it completed
  Typing a slash command opens the completion menu over the composer. Sending
  that draft with the Send button, or queueing it while a turn runs, empties
  the composer from outside: the textarea fires no change event for a value its
  parent set, so the menu stayed open over the empty composer until the next
  keystroke. The composer now closes its pickers whenever the draft is emptied.

  Scenario: Sending or queueing a command closes its menu
    Then Send clicked while the slash menu is open closes the menu
    And a draft queued while the slash menu is open closes the menu
