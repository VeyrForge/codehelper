const { app, BrowserWindow, ipcMain } = require("electron");
const path = require("path");

function greet(name) {
  return `hello ${name}`;
}

function handleGreet(_event, name) {
  return greet(name || "electron");
}

function createWindow() {
  const win = new BrowserWindow({
    webPreferences: {
      preload: path.join(__dirname, "../preload/preload.js"),
    },
  });
  win.loadFile(path.join(__dirname, "../renderer/index.html"));
}

app.whenReady().then(() => {
  ipcMain.handle("greet", handleGreet);
  createWindow();
});
