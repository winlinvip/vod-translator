# VoD Translator

!!! Warning: We have integrated this project to [Oryx(SRS Stack)](https://github.com/ossrs/oryx), see [Revolutionize Video Content with Oryx: Effortless Dubbing and Translating to Multiple Languages Using OpenAI](https://blog.ossrs.io/expand-your-global-reach-with-srs-stack-effortless-video-translation-and-dubbing-solutions-544e1db671c2), to make it more easy to use.

VoD Translator uses GPT to translate speech in video to other languages.

## Usage

Setup a `.env` file with the following variables:

```
OPENAI_API_KEY=xxx
OPENAI_PROXY=https://api.openai.com/v1
VODT_ASR_LANGUAGE=en
```

Then start the backend:

```bash
cd backend && go run .
```

Next, start the frontend:

```bash
npm install
npm run start
```

Finally, open http://localhost:3000/ in your browser.

## Translate a VoD File

First, Put the file like `ai-talk.mp4` in the `backend/static` folder.

```bash
cp /path/your-file.mp4 backend/static/ai-talk.mp4
```

Second, click the button `Create` to create a project.

Next, input the url `/api/vod-translator/resources/ai-talk.mp4` in the input box.

Finally, click the button `Load` to load the file.

