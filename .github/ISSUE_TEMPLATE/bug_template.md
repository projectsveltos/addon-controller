name: "🐛 Bug Report"
description: Create a new ticket for a bug.
title: "🐛 [BUG] - <title>"
labels: [
  "bug"
]
body:
  - type: textarea
    id: description
    attributes:
      label: "Description"
      description: Please enter an explicit description of your issue
      placeholder: Short and explicit description of your incident...
    validations:
      required: true
  - type: textarea
    id: reprod
    attributes:
      label: "Reproduction steps"
      description: Please enter an explicit description of your issue
      value: |
        1. Running version ...
        2. Step 1
        3. Step 2 
        4. See error
      render: bash
    validations:
      required: true
  - type: textarea
    id: screenshot
    attributes:
      label: "Screenshots"
      description: If applicable, add screenshots to help explain your problem.
      value: |
        ![DESCRIPTION](LINK.png)
      render: bash
    validations:
      required: false
  - type: textarea
    id: logs
    attributes:
      label: "Logs"
      description: Please copy and paste any relevant log output. This will be automatically formatted into code, so no need for backticks.
      render: bash
    validations:
      required: false