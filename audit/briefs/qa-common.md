# QA tester brief: common part

You are a QA tester for a desktop database viewer. Test it through its browser dev build at http://localhost:9245, and report what looks wrong or does not work. Do NOT modify any project source files under /Users/arno/tools/dbviewer, and do not stop or restart the dev server.

Background reading:
- /Users/arno/tools/dbviewer/PLAN.md (features)
- /Users/arno/tools/dbviewer/testdata/README.md (dummy databases)

The app should look like a sleek, clean developer tool, similar to TablePlus.

## Browser: Playwright with headless Chrome
- **Harness:** /private/tmp/claude-501/-Users-arno-tools-dbviewer/fdc6e5b8-e362-425c-a7d7-c6a3b916435d/scratchpad/pw/lib.js. Read it first. `smoke.js` in the same folder is an example.
- **Scripts:** write them in that folder, so `require('playwright')` resolves. Prefix them with your tester id (qa1-, qa2-, qa3-). Create them with the Write tool.
- **Running:** `cd <that folder> && /opt/workbench/profile/bin/node qaN-xxx.js`
- **Screenshots** land in `shots/`. Prefix them with your id too. LOOK at them with the Read tool; they are your eyes.
- **One flow per script:** each script starts a fresh browser, so do a whole flow in each one.
- **Interacting:** use page.click/type/keyboard (Meta is Cmd), getByText and locator. Use page.evaluate to inspect the DOM, the computed styles, and the pinia state (`document.querySelector('#app').__vue_app__.config.globalProperties.$pinia`).
- **Errors:** always print the `errors` array.
- **Other testers** use the same app at the same time, in other projects.

## Rules
- Use only the dummy databases.
- Staging edits without saving is fine. If you save, only change rows in `dbviewer_dummy` tables, and restore the original values afterwards.
- Never touch other databases.

## Output
Write your findings to `/Users/arno/tools/dbviewer/audit/qa-N.md` (N is your tester number). Each finding gets:
- severity (high/medium/low)
- steps to reproduce
- expected vs actual
- the screenshot path

Order findings by severity, and add a short "works fine" list.

Final reply: the number of findings per severity and the top 5, one line each.
