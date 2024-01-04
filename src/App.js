import React from "react";
import './App.css';
import axios from "axios";
import {BrowserRouter, Route, Routes, useLocation, useNavigate} from "react-router-dom";
import { saveAs } from 'file-saver';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path='*' element={<AppRoot/>} />
        <Route path='/create' element={<Create/>} />
        <Route path='/project' element={<Project/>} />
      </Routes>
    </BrowserRouter>
  );
}

function AppRoot() {
  const navigate = useNavigate();
  React.useEffect(() => {
    navigate('/create');
  }, [navigate]);
  return <></>;
}

function Create() {
  const location = useLocation();
  const navigate = useNavigate();
  const createStage = React.useCallback(() => {
    axios.post("/api/vod-translator/create/").then(res => {
      const sid = res.data.data.sid;

      const params = new URLSearchParams(location.search);
      params.set('sid', sid);
      navigate(`/project?${params}`);

      console.log(`Stage created, sid=${sid}`);
    });
  }, [navigate]);

  return <>
    <div style={{padding: '10px'}}>
      <button onClick={() => createStage()}>Create</button>
    </div>
  </>;
}

function Project() {
  const location = useLocation();
  const navigate = useNavigate();
  const [loading, setLoading] = React.useState(true);
  const [sid, setSid] = React.useState(null);
  const [inputURL, setInputURL] = React.useState(null);

  React.useEffect(() => {
    const params = new URLSearchParams(location.search);
    if (!params.get('sid')) {
      navigate('/create');
    } else {
      setSid(params.get('sid'));
    }
  }, [location, navigate, setSid]);

  React.useEffect(() => {
    if (!sid) return;

    axios.post("/api/vod-translator/load/", {
      sid,
    }).then(res => {
      const url = res.data.data.url;
      if (url) setInputURL(url);
      setLoading(false);
    });
  }, [sid, setLoading, setInputURL]);

  if (loading || !sid) {
    return <>Loading...</>;
  }
  return <Editor sid={sid} defaultInputURL={inputURL} />;
}

function Editor({sid, defaultInputURL}) {
  const player = React.useRef(null);
  const ttsPlayer = React.useRef(null);
  const [inputUrl, setInputUrl] = React.useState(defaultInputURL || '/api/vod-translator/resources/ai-talk.mp4');
  const [asr, setAsr] = React.useState(null);
  // Automatically or user selected segment.
  const previousSelectedSegment = React.useRef(null);
  // Whether video loaded.
  const [loading, setLoading] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);

  const formatDuration = React.useCallback((duration) => {
    let hours = Math.floor(duration / 3600);
    let minutes = Math.floor((duration - (hours * 3600)) / 60);
    let seconds = duration - (hours * 3600) - (minutes * 60);
    let milliseconds = Math.round((seconds % 1) * 1000);

    hours = hours < 10 ? "0"+hours : parseInt(hours);
    minutes = minutes < 10 ? "0"+minutes : parseInt(minutes);
    seconds = seconds < 10 ? "0"+parseInt(seconds) : parseInt(seconds);
    milliseconds = milliseconds < 100 ? (milliseconds < 10 ? "00"+parseInt(milliseconds) : "0" + parseInt(milliseconds)) : parseInt(milliseconds);

    return hours+':'+minutes+':'+seconds+'.'+milliseconds;
  }, []);

  const loadVideo = React.useCallback(() => {
    setLoading(true);
    axios.post("/api/vod-translator/asr/", {
      sid, url: inputUrl,
    }).then(res => {
      console.log(`ASR result ${JSON.stringify(res.data.data)}`);
      player.current.src = inputUrl;
      setAsr(res.data.data.asr);
      setLoaded(true);
    }).finally(() => {
      setLoading(false);
    });
  }, [player, inputUrl, setAsr, setLoading, setLoaded]);

  const playSegemnt = React.useCallback((e, segment) => {
    player.current.currentTime = segment.start;
    player.current.play();
  }, []);

  const editSegment = React.useCallback((e, segment) => {
    segment.update = new Date().toISOString();
    segment.editing = true;
    setAsr({...asr, segments: asr.segments.map(s => {
        if (s.id === segment.id) {
          return {...segment};
        }
        return s;
      })});
  }, [asr, setAsr]);

  const saveText = React.useCallback((e, segment) => {
    segment.text = e.target.value;
    setAsr({...asr, segments: asr.segments.map(s => {
        if (s.id === segment.id) {
          return {...segment};
        }
        return s;
      })});
  }, [asr, setAsr]);

  const saveTranslated = React.useCallback((e, segment) => {
    segment.translated = e.target.value;
    setAsr({...asr, segments: asr.segments.map(s => {
        if (s.id === segment.id) {
          return {...segment};
        }
        return s;
      })});
  }, [asr, setAsr]);

  const saveSegment = React.useCallback((e, segment) => {
    segment.update = new Date().toISOString();
    segment.editing = false;

    const newAsr = {...asr, segments: asr.segments.map(s => {
      if (s.id === segment.id) {
        return {...segment};
      }
      return s;
    })};

    axios.post("/api/vod-translator/asr-update/", {
      sid, segment,
    }).then(res => {
      setAsr(newAsr);
      console.log(`ASR update ok`);
    });
  }, [asr, setAsr]);

  const removeSegment = React.useCallback((e, segment) => {
    segment.update = new Date().toISOString();
    segment.removed = true;

    const newAsr = {...asr, segments: asr.segments.map(s => {
        if (s.id === segment.id) {
          return {...segment};
        }
        return s;
      })};

    axios.post("/api/vod-translator/asr-update/", {
      sid, segment,
    }).then(res => {
      setAsr(newAsr);
      console.log(`ASR removed ok`);
    });
  }, [asr, setAsr]);

  const translateSegment = React.useCallback((e, s) => {
    axios.post("/api/vod-translator/translate/", {
      sid, segment: s,
    }).then(res => {
      s = res.data.data.segment;
      setAsr({...asr, segments: asr.segments.map(s0 => {
          if (s0.id === s.id) {
            return {...s};
          }
          return s0;
        })});
      console.log(`Translate ${s} ok`);
    });
  }, [asr, setAsr]);

  const shorterSegment = React.useCallback((e, s) => {
    axios.post("/api/vod-translator/shorter/", {
      sid, segment: s,
    }).then(res => {
      s = res.data.data.segment;
      setAsr({...asr, segments: asr.segments.map(s0 => {
          if (s0.id === s.id) {
            return {...s};
          }
          return s0;
        })});
      console.log(`Make shorter ${s} ok`);
    });
  }, [asr, setAsr]);

  const translateAll = React.useCallback(async () => {
    for (let i = 0; i < asr.segments.length; i++) {
      let s = asr.segments[i];
      if (s.removed) continue;
      if (s.translated) continue;

      const doTranslate = async () => {
        return new Promise((resolve, reject) => {
          axios.post("/api/vod-translator/translate/", {
            sid, segment: s,
          }).then(res => {
            asr.segments[i] = s = res.data.data.segment;
            console.log(`Translate ${JSON.stringify(s)} ok`);
            resolve();
          }).catch(e => {
            console.log(`Translate ${JSON.stringify(s)} failed`);
            reject(e);
          });
        });
      };

      // Translate with retry.
      for (let i = 0; i < 3; i++) {
        try {
          await doTranslate();
          break;
        } catch (e) {
          console.log(`Translate ${JSON.stringify(s)} failed, retry ${i}`);
          await new Promise(resolve => setTimeout(resolve, 1500));
        }
      }

      setAsr({...asr, segments: asr.segments.map(s0 => {
          if (s0.id === s.id) {
            return {...s};
          }
          return s0;
        })});

      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }, [asr, setAsr]);

  const ttsAll = React.useCallback(async () => {
    for (let i = 0; i < asr.segments.length; i++) {
      let s = asr.segments[i];
      if (s.removed) continue;
      if (s.tts) continue;

      await new Promise(resolve => {
        axios.post("/api/vod-translator/tts/", {
          sid, segment: s,
        }).then(res => {
          asr.segments[i] = s = res.data.data.segment;
          console.log(`TTS ${s} ok`);
          resolve();
        });
      });

      setAsr({...asr, segments: asr.segments.map(s0 => {
          if (s0.id === s.id) {
            return {...s};
          }
          return s0;
        })});

      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }, [asr, setAsr]);

  const ttsSegment = React.useCallback((e, s) => {
    axios.post("/api/vod-translator/tts/", {
      sid, segment: s,
    }).then(res => {
      s = res.data.data.segment;
      setAsr({...asr, segments: asr.segments.map(s0 => {
          if (s0.id === s.id) {
            return {...s};
          }
          return s0;
        })});
      console.log(`TTS ${s} ok`);
    });
  }, [asr, setAsr]);

  const mergeNext = React.useCallback((e, s) => {
    let index = asr.segments.findIndex(segment => segment.id === s.id);
    if (index < 0) return alert(`no segment`);

    let next = asr.segments[index + 1];
    if (!next || next.removed) return alert(`no next`);

    axios.post("/api/vod-translator/merge/", {
      sid, segment: s, next,
    }).then(res => {
      asr.segments[index] = s = res.data.data.segment;
      asr.segments.splice(index + 1, 1);
      setAsr({...asr, segments: [...asr.segments]});
      console.log(`Merge ${s} with ${next} ok`);
    });
  }, [asr, setAsr]);

  const previewTTS = React.useCallback((e, s) => {
    player.current.currentTime = s.start;
    ttsPlayer.current.src = `/api/vod-translator/preview/${sid}/${s.uuid}/${s.tts}?t=${new Date().getTime()}`;
    ttsPlayer.current.play();
  }, [player, ttsPlayer]);

  const restoreSegment = React.useCallback((e, segment) => {
    segment.update = new Date().toISOString();
    segment.removed = false;

    const newAsr = {...asr, segments: asr.segments.map(s => {
        if (s.id === segment.id) {
          return {...segment};
        }
        return s;
      })};

    axios.post("/api/vod-translator/asr-update/", {
      sid, segment,
    }).then(res => {
      setAsr(newAsr);
      console.log(`ASR restore ok`);
    });
  }, [asr, setAsr]);

  const exportAudio = React.useCallback(() => {
    axios.post("/api/vod-translator/export/", {
      sid,
    }, {
      responseType: 'blob',
    }).then(res => {
      const blob = new Blob([res.data], { type: 'video/mp4' });
      saveAs(blob, 'audio.mp4');
      console.log(`Export ok`);
    });
  }, []);

  // Auto select segment if player is playing.
  React.useEffect(() => {
    const timer = setInterval(() => {
      if (!player?.current?.currentTime) return;
      if (!asr?.segments) return;

      let segment = asr?.segments?.find(s => s.start <= player.current.currentTime && player.current.currentTime <= s.end);
      if (!segment) return;
      if (previousSelectedSegment.current === segment) return;
      if (asr.segments.indexOf(segment) && player.current.currentTime <= 0) return;

      const tr = document.querySelector(`tr:nth-child(${asr.segments.indexOf(segment) + 1})`);
      const previousTr = document.querySelector(`tr:nth-child(${asr.segments.indexOf(previousSelectedSegment.current) + 1})`);
      let scroolToTr = document.querySelector(`tr:nth-child(${asr.segments.indexOf(segment) - 3})`);
      if (asr.segments.indexOf(segment) <= 0) scroolToTr = tr;
      if (!player.current.paused && scroolToTr) scroolToTr.scrollIntoView({behavior: 'smooth'});
      if (previousSelectedSegment.current && previousTr) {
        previousTr.style.backgroundColor = '';
      }
      if (tr) {
        tr.style.backgroundColor = 'yellow';
        previousSelectedSegment.current = segment;
      }

      console.log(`Player time ${player.current.currentTime}, segment is ${segment?.id}, ${segment?.start} ~ ${segment?.end}`);
    }, 600);
    return () => clearInterval(timer);
  }, [asr, player]);

  return (
    <div className='editor-container'>
      {!loaded && <p>
        Input: &nbsp;
        <input type='input' value={inputUrl} className='video-url-input'
               onChange={e => setInputUrl(e.target.value)}/> &nbsp;
        <button onClick={() => loadVideo()} disabled={loading}>Load</button>
        &nbsp;
      </p>}
      <p>
        <video ref={player} controls className='video-player' hidden={!loaded}></video>
        <audio ref={ttsPlayer} hidden={true}></audio>
      </p>
      {asr ? <p>
        {asr?.language} {asr?.duration}s &nbsp;
        <button onClick={(e) => translateAll()}>翻译全部</button> &nbsp;
        <button onClick={(e) => ttsAll()}>TTS全部</button> &nbsp;
        <button onClick={(e) => exportAudio()}>导出</button> &nbsp;
      </p> : ''}
      {asr ? <table>
        <tbody>
        {asr?.segments?.map((s, index) => {
          return (
            <tr key={s.id} style={{
              textDecoration: s.removed ? 'line-through' : '',
              textDecorationStyle: 'double',
              backgroundColor: !s.removed && s.tts_duration && s.tts_duration > (s.end - s.start) ? 'red':'',
            }}>
              <td>{index}</td>
              <td onClick={(e) => playSegemnt(e, s)}>{s.uuid.substr(0, 8)}</td>
              <td>{formatDuration(s.start)} ~ {formatDuration(s.end)}</td>
              <td>{Number(s.end - s.start).toFixed(1)}</td>
              <td onClick={(e) => previewTTS(e, s)}>{Number(s.tts_duration).toFixed(1)}</td>
              {s.editing ?
                <td>
                  <input defaultValue={s.text} style={{width: '500px'}} onChange={(e) => saveText(e, s)}></input> <br/>
                  <input defaultValue={s.translated} style={{width: '500px'}} onChange={(e) => saveTranslated(e, s)}></input>
                </td> :
                <td style={{width: '40%'}} onClick={(e) => playSegemnt(e, s)}>
                  {s.text} <br/>
                  {s.translated}
                </td>}
              <td>
                {!s.removed ? '' : <><button onClick={(e) => restoreSegment(e, s)}>Restore</button></>}
                {s.removed || s.editing ? '' : <><button onClick={(e) => editSegment(e, s)}>Edit</button>&nbsp;</>}
                {s.removed || !s.editing ? '' : <><button onClick={(e) => saveSegment(e, s)}>Save</button>&nbsp;</>}
                {s.removed || s.editing ? '' : <><button onClick={(e) => removeSegment(e, s)}>Delete</button>&nbsp;</>}
                {s.removed || s.editing ? '' : <><button onClick={(e) => translateSegment(e, s)}>Translate</button>&nbsp;</>}
                {s.removed || s.editing ? '' : <><button onClick={(e) => shorterSegment(e, s)}>Shorter</button>&nbsp;</>}
                {s.removed || s.editing ? '' : <><button onClick={(e) => ttsSegment(e, s)}>TTS</button>&nbsp;</>}
                {s.removed || s.editing || index === asr?.segments?.length - 1 || asr?.segments[index+1]?.removed ? '' :
                  <><button onClick={(e) => mergeNext(e, s)}>MergeNext</button>&nbsp;</>}
              </td>
            </tr>
          );
        })}
        </tbody>
      </table> : ''}
    </div>
  );
}

export default App;
