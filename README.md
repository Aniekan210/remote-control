# Remote Control

**Control your computer from anywhere.**

Remote Control lets you tell your Windows computer what to do in plain language, from your phone, without sitting in front of it.

Forgot to send an email? Need to download a file? Remember something you wanted to do on your computer while you're away from your desk?

Open Remote Control on your phone, type or say what you want, and watch your computer do it, step by step. If it gets stuck or is about to do something it can't undo, it asks you first.

Built for lazy engineers, people who remember things at the worst possible time, and anyone who has ever thought *"I'll do that when I get back to my computer"* and then completely forgotten.

## How It Works

Remote Control uses **two AI models** to turn a natural-language request into actual computer actions, and checks the screen before every step so it can recover when something unexpected happens.

### 1. Planning

The first model works out **what needs to happen**.

When a task starts, the planner gets your request along with context about the computer:

* A screenshot of the current screen
* A short, filtered list of the files and folders on your computer

The desktop worker runs a lightweight **filesystem watcher**. It never sends file contents anywhere. When a task starts it sends only the entries that matter: the folders you keep things in, the files whose names match your request, and the files you touched most recently. That keeps the AI grounded in what's actually on your computer while keeping every request small and cheap.

The planner breaks your request into small, single-purpose steps, one change on screen at a time.

For example:

> "Download the Hack Atlantic schedule and put it in my Hackathon folder."

The planner can see where your Hackathon folder is and write that into its plan. It also marks steps that can't be undone, such as sending, submitting, deleting, buying or posting, so they wait for your approval.

### 2. Execution

The second model takes the current step and looks at the **current screen through a screenshot** to work out exactly what to do.

It turns the step into precise computer interactions:

* Moving the mouse
* Left-clicking and right-clicking
* Typing text and pressing keys and shortcuts
* Waiting for something to finish loading

Before acting, it checks the screen matches what the plan expects:

* **Small interruptions** like cookie banners, popups and "not now" prompts are cleared automatically.
* **The step is already done** on screen, so it's skipped.
* **Something unexpected** happened, like the wrong page or a missing button, so the plan is revised on the spot from what's actually on screen.
* **The task can't be done as asked**, for example you're out of invites, a login is required, or the item doesn't exist. It stops and **asks you on your phone**, with a picture of the screen, and carries on with your answer.

Once the last step runs, it takes one more look at the screen to confirm the whole task is really done before calling it finished.

Remote Control does not rely on application-specific APIs. The AI uses the graphical interface itself, with the same inputs a person would: **the screen, mouse, and keyboard.**

The desktop worker carries out the actions on your computer while the AI handles the reasoning. After each set of actions it waits for the screen to settle and sends the new state back, so every decision is made from what's really on screen.

## Getting Started

Remote Control currently supports **Windows only**.

### 1. Create an Account

Go to:

**control.aniekan.dev**

Sign in with Google.

Then open **Settings** and add your own **OpenRouter API key**, which is what pays for the AI. It's stored encrypted and never shown again. Set a credit limit on the key in OpenRouter so a task can never spend more than you expect.

### 2. Install the Desktop Worker

Download and run:

the unlogged `.exe` from [the latest release](https://github.com/Aniekan210/remote-control/releases/latest)

The desktop worker runs on the computer you want Remote Control to control. Windows may warn you about an unrecognised app the first time; choose **More info → Run anyway**.

### 3. Link Your Computer

After starting the desktop worker, it will display a **QR code**.

Open **control.aniekan.dev** and scan the QR code.

This links your Remote Control account to your computer.

### 4. Start Controlling Your Computer

Once your device is linked, you can give your computer instructions directly from the web app.

You can use the web app from another computer, a tablet, or your phone. Type your request or use the mic.

You'll see the plan, every step, and every action as it happens. While a task runs, the controlled computer shows a glowing edge and a small status pill with the current step, so anyone at the desk knows the machine is driving.

When Remote Control needs you, your phone buzzes and shows the question with a picture of the screen:

* **Approve** a step that can't be undone, or tell it to do something else instead
* **Answer** when it's stuck ("send the invite without a note")
* **Continue** when a task has used a lot of time or AI calls

## Stopping a Task

Remote Control is designed to let you take control back from the AI immediately.

**Move the mouse on the controlled computer and the current task pauses.**

The desktop worker notices real mouse movement and pauses the task so you're never fighting it for the cursor. Leave the mouse alone for a few seconds and it carries on. Keep moving it and the task stays paused until you resume it from the app.

**Press Ctrl + Alt + Shift + X on the controlled computer to cancel the task outright**, even if your phone is offline.

You can also pause, resume, or cancel a task from the web app.

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
* Works on your main monitor only (the task's window is moved there when it starts)
* Requires the desktop worker to be running on the computer being controlled
* Requires an internet connection
* Requires your own OpenRouter API key; you pay OpenRouter directly for what your tasks use
* Computer interaction is limited to mouse and keyboard input
* AI performance depends on the underlying vision and language models

## Project

Remote Control was built during a 24-hour hackathon with the goal of exploring how AI can interact with computers through their existing graphical interfaces rather than relying entirely on application-specific APIs.

**Tell your computer what to do.**
