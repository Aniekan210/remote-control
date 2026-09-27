# Remote Control

**Control your computer from anywhere.**

Remote Control is a web-based computer control system that lets you tell your computer what to do using natural language, without physically sitting in front of it.

Forgot to send an email? Need to download a file? Remember something you wanted to do on your computer while you're away from your desk?

Open Remote Control on your phone, tell your computer what to do, and let it handle the rest.

Built for lazy engineers, people who remember things at the worst possible time, and anyone who has ever thought *"I'll do that when I get back to my computer"* and then completely forgotten.

## How It Works

Remote Control uses **two AI models** to turn a natural-language request into actual computer actions.

### 1. Planning

The first model determines **what needs to happen**.

When a task starts, Remote Control provides the planner with the user's request along with context about the computer.

This context can include:

* A screenshot of the current screen
* The user's saved preferences and aliases
* Information about the computer's filesystem

Remote Control maintains a lightweight **filesystem watcher** on the desktop computer. Instead of sending file contents around, it keeps track of the structure and metadata of the filesystem so the AI can understand what files and directories are available when planning a task.

The planner uses this context to break the user's request into a sequence of instructions.

For example:

> "Download the Hack Atlantic schedule and put it in my Hackathon folder."

The model can use the available filesystem context to understand where that folder exists and incorporate that information into its plan.

### 2. Execution

The second model takes the current instruction and looks at the **current screen through a screenshot** to determine exactly what needs to happen.

It translates the instruction into precise computer interactions such as:

* Moving the mouse
* Left-clicking
* Right-clicking
* Typing text

Remote Control does not rely entirely on application-specific APIs to control your computer. The AI interacts with the graphical interface itself, using the same basic inputs a person would use: **the screen, mouse, and keyboard.**

The desktop worker is responsible for executing these actions on your computer while the AI handles the reasoning.

After each set of actions is executed, the worker provides the latest screen state so Remote Control can continue working from the computer's current state.

## Getting Started

Remote Control currently supports **Windows only**.

### 1. Create an Account

Go to:

**control.aniekan.dev**

Create an account and open the Remote Control dashboard.

### 2. Install the Desktop Worker

Download and run:

`/desktop-worker/builds/remoteworker-unlogged-windows-build-v1.exe`

The desktop worker runs on the computer you want Remote Control to control.

### 3. Link Your Computer

After starting the desktop worker, it will display a **QR code**.

Open **control.aniekan.dev** and scan the QR code.

This links your Remote Control account to your computer.

### 4. Start Controlling Your Computer

Once your device is linked, you can give your computer instructions directly from the web app.

You can use the web app from another computer, tablet, or your phone.

Type what you want your computer to do and Remote Control will handle the rest.

## Stopping a Task

Remote Control is designed to let you immediately take control back from the AI.

**Move the mouse on the controlled computer and the current task will stop.**

The desktop worker detects the mouse movement and stops the current task, allowing the user to take control of their computer again.

You can also pause, resume, or cancel a task from the web application

## Why?

Remote Control is built around a simple idea:

**Your computer should be able to do things for you even when you're not sitting in front of it.**

Maybe you're on the couch.

Maybe you're walking somewhere.

Maybe you're on your phone.

Maybe you remembered something important while doing absolutely nothing productive.

Instead of waiting until you're back at your computer, just open Remote Control and tell your computer what to do.

## Current Limitations

* **Windows only**
* Requires the desktop worker to be running on the computer being controlled
* Requires an internet connection
* Computer interaction is currently limited to mouse and keyboard input
* AI performance depends on the underlying vision and language models

## Project

Remote Control was built during a 24-hour hackathon with the goal of exploring how AI can interact with computers through their existing graphical interfaces rather than relying entirely on application-specific APIs.

**Tell your computer what to do.**
