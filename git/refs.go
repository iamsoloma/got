package git

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"got/utils"
	"io"
	"strconv"
	"strings"
)

type Head struct {
	Ref string
}

func (r *Repository) ReadHead() (Head, error) {
	body, err := r.Storage.GetHEAD()
	if err != nil {
		return Head{}, err
	}

	ref, _ := strings.CutPrefix(body, "ref: ")

	return Head{Ref: ref}, nil
}

func (r *Repository) UpdateHead(ref string) error {
	body := fmt.Sprintf("ref: %s", ref)
	err := r.Storage.SetHEAD(body)

	if err != nil {
		return err
	}

	return nil
}

type Reference struct {
	Name string
	Body string
	//Sha1 string
}

func (r *Repository) ListLocalBranches() ([]Reference, error) {
	var branches []Reference
	refsNames, err := r.Storage.ListReferences()
	if err != nil {
		return nil, err
	}

	for _, name := range refsNames {
		ref, err := r.Storage.GetReference(name)
		if err != nil {
			return nil, err
		}
		branches = append(branches, Reference{Name: name, Body: ref})
	}

	return branches, nil
}

func (r *Repository) UpdateReference(ref Reference) error {
	return r.Storage.SetReference(ref.Name, ref.Body)
}

func (r *Repository) ReadReference(name string) (Reference, error) {
	content, err := r.Storage.GetReference(name)
	if err != nil {
		return Reference{}, err
	}

	return Reference{Name: name, Body: content}, nil
}

type Tag struct {
	Name string
	Sha1 string
}

func (r *Repository) CreateTag(tag Tag) error {
	err := r.Storage.SetReference("/tags/"+tag.Name, tag.Sha1)
	if err != nil {
		return err
	}
	return nil
}

func (r *Repository) ReadTag(name string) (tag Tag, err error) {
	tag.Sha1, err = r.Storage.GetReference("/tags/" + name)
	if err != nil {
		return tag, err
	}
	tag.Name = name
	return tag, nil
}

type AnnotatedTag struct {
	Name             string
	TaggedObjectSha1 string
	TaggedObjectType string
	Tagger           Tagger
	Message          string
}

type Tagger struct {
	Name      string
	Email     string
	Timestamp int64
	Timezone  int
}

func (r *Repository) ReadAnnotatedTag(name string) (tag AnnotatedTag, err error) {
	tag.Name = name

	refBody, err := r.Storage.GetReference("/tags/" + name)
	if err != nil {
		return tag, fmt.Errorf("can`t get reference: %s", err.Error())
	}

	file, err := r.Storage.ObjectReader(refBody)
	if err != nil {
		return tag, fmt.Errorf("can`t open object: %s", err.Error())
	}
	defer file.Close()

	reader, err := zlib.NewReader(file)
	if err != nil {
		return tag, err
	}

	content, err := io.ReadAll(reader)
	if err != nil {
		return tag, err
	}
	reader.Close()

	//read object type
	tagHeader := []byte("tag ")
	if !bytes.HasPrefix(content, tagHeader) {
		return tag, errors.New("not correct tag header: " + string(content[:len(tagHeader)]))
	}

	delimIndex := bytes.Index(content, []byte{0})

	size, err := strconv.Atoi(string(content[len(tagHeader):delimIndex]))
	if err != nil {
		return tag, errors.New("not correct tag header: " + string(content[:delimIndex]))
	}
	content = content[delimIndex+1:]

	if size != len(content) {
		return tag, fmt.Errorf("not correct tag size in header. Expected: %d. Actual: %d", len(content), size)
	}

	//read object`s tag
	if !bytes.HasPrefix(content, []byte("object ")) {
		return tag, fmt.Errorf("not correct tag object: not found tagged object declaration")
	}

	objectShaDelim := bytes.Index(content, []byte(" "))
	if objectShaDelim == -1 {
		return tag, fmt.Errorf("not correct tag object: not found delimeter between object declaration and his sha")
	}

	nextStroke := bytes.Index(content, []byte("\n"))
	if nextStroke == -1 {
		return tag, fmt.Errorf("not correct tag object: not found end of first line with tagged object`s sha")
	}

	sha := string(content[objectShaDelim+1 : nextStroke])
	tag.TaggedObjectSha1 = sha

	content = content[nextStroke+1:]

	// read tagged object`s type
	typeHeader := bytes.HasPrefix(content, []byte("type "))
	if !typeHeader {
		return tag, fmt.Errorf("not correct tag object: not found header of tagged object type")
	}

	objectTypeDelim := bytes.Index(content, []byte(" "))
	if objectShaDelim == -1 {
		return tag, fmt.Errorf("not correct tag object: not found delimeter between object type declaration and his type")
	}

	nextStroke = bytes.Index(content, []byte("\n"))
	if nextStroke == -1 {
		return tag, fmt.Errorf("not correct tag object: not found end of second line with tagged object`s type")
	}

	objType := string(content[objectTypeDelim+1 : nextStroke])
	tag.TaggedObjectType = objType

	content = content[nextStroke+1:]

	// read tag name
	tagNameHeader := bytes.HasPrefix(content, []byte("tag "))
	if !tagNameHeader {
		return tag, fmt.Errorf("not correct tag object: not found header of tag name")
	}

	objectTagDelim := bytes.Index(content, []byte(" "))
	if objectShaDelim == -1 {
		return tag, fmt.Errorf("not correct tag object: not found delimeter between object tag name declaration and his name")
	}

	nextStroke = bytes.Index(content, []byte("\n"))
	if nextStroke == -1 {
		return tag, fmt.Errorf("not correct tag object: not found end of third line with tag`s name")
	}

	tagName := string(content[objectTagDelim+1 : nextStroke])
	if tag.Name != tagName {
		return tag, fmt.Errorf("not correct tag object: reference name does not match object name.")
	}
	tag.TaggedObjectType = objType

	content = content[nextStroke+1:]

	//read tagger
	taggerHeader := bytes.HasPrefix(content, []byte("tagger "))
	if !taggerHeader {
		return tag, fmt.Errorf("not correct tag object: not found header of tagger")
	}

	objectTaggerDelim := bytes.Index(content, []byte(" "))
	if objectShaDelim == -1 {
		return tag, fmt.Errorf("not correct tag object: not found delimeter between tagger declaration and his data")
	}

	nextStroke = bytes.Index(content, []byte("\n"))
	if nextStroke == -1 {
		return tag, fmt.Errorf("not correct tag object: not found end of fourth line with tagger data")
	}

	taggerData := string(content[objectTaggerDelim+1 : nextStroke])

	tag.Tagger, err = readTaggerData(taggerData)
	if err != nil {
		return tag, fmt.Errorf("not correct tag object: problem in tagger`s line: %s", err.Error())
	}

	content = content[nextStroke+2:]

	//read message
	if len(content) != 0 {
		endOfTag := bytes.Index(content, []byte("\n"))
		if endOfTag == -1 {
			return tag, fmt.Errorf("not correct tag object: not found end of tag`s message")
		}
		tag.Message = string(content[:endOfTag])
	}

	return tag, nil
}

func readTaggerData(inp string) (tagger Tagger, err error) {
	emailStart := strings.Index(inp, " <")
	if emailStart == -1 {
		return tagger, fmt.Errorf("can`t find start of email")
	}
	tagger.Name = inp[:emailStart]

	emailEnd := strings.Index(inp, ">")
	if emailEnd == -1 {
		return tagger, fmt.Errorf("can`t find end of email")
	}

	tagger.Email = inp[emailStart+2 : emailEnd]

	inp = inp[emailEnd+2:]

	timeDelimeter := strings.Index(inp, " ")
	if timeDelimeter == -1 {
		return tagger, fmt.Errorf("can`t find delimeter between timestamp and timezone")
	}

	tagger.Timestamp, err = strconv.ParseInt(inp[:timeDelimeter], 10, 64)
	if err != nil {
		return tagger, fmt.Errorf("can`t parse timestamp")
	}

	tagger.Timezone, err = utils.ParseTimezone(inp[timeDelimeter+1:])
	if err != nil {
		return tagger, fmt.Errorf("can`t parce timezone")
	}

	return tagger, nil
}
