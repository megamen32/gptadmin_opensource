document.getElementById("connect").addEventListener("click", async () => {
  const secret = document.getElementById("secret").value.trim();
  if (!/^[a-f0-9]{64}$/.test(secret)) { document.getElementById("status").textContent = "Нужен код из 64 шестнадцатеричных символов"; return; }
  try { await chrome.runtime.sendMessage({type:"connect", secret}); document.getElementById("status").textContent = "Соединение запускается"; }
  catch (e) { document.getElementById("status").textContent = String(e); }
});
