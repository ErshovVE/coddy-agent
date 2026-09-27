Feature: The model looks at an image file with view_image
  The read tool refuses binary files, so view_image is how the model sees a
  screenshot or a rendered page from the workspace. A tool result cannot
  carry image content in the OpenAI-compatible schema, so the images a batch
  of tool calls queued reach the model as one user message after the whole
  batch: the tool results still follow the assistant message that asked for
  them, one per call, and nothing comes between them. The tool is offered
  only to a model configured to read images (models[].multimodal).

  Scenario: Two images viewed in one batch arrive together after the tool results
    Given a model that reads images
    And workspace images "before.png" and "after.png"
    When the model views "before.png" and "after.png" in one batch, then answers
    Then the next LLM request has the assistant tool calls, then both tool results, then one user message
    And that user message carries the images "before.png" and "after.png" in that order
    And the persisted transcript keeps that user message with both images

  Scenario: A file that is not an image is refused and nothing is attached
    Given a model that reads images
    And a workspace file "notes.txt"
    When the model views "notes.txt", then answers
    Then the tool result says "is not a PNG, JPEG, GIF or WebP image"
    And the next LLM request carries no images

  Scenario: A model that does not read images is not offered the tool and a call is refused
    Given a model that does not read images
    And workspace images "before.png" and "after.png"
    When the model views "before.png", then answers
    Then the first LLM request does not offer view_image
    And the tool result says "does not read images"
    And the next LLM request carries no images

  Scenario: A turn cancelled in the middle of a batch drops the images it queued
    Given a model that reads images
    And workspace images "before.png" and "after.png"
    When the model views "before.png" and the turn is cancelled before the batch ends
    And the user sends the next prompt and the model lists the workspace, then answers
    Then the next LLM request carries no images
