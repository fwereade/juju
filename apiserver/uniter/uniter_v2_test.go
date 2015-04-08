// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package uniter_test

import (
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/apiserver/params"
	apiservertesting "github.com/juju/juju/apiserver/testing"
	"github.com/juju/juju/apiserver/uniter"
	"github.com/juju/juju/state"
)

//TODO run all common V0 and V1 tests.
type uniterV2Suite struct {
	uniterBaseSuite
	uniter *uniter.UniterAPIV2
}

var _ = gc.Suite(&uniterV2Suite{})

func (s *uniterV2Suite) SetUpTest(c *gc.C) {
	s.uniterBaseSuite.setUpTest(c)

	uniterAPIV2, err := uniter.NewUniterAPIV2(
		s.State,
		s.resources,
		s.authorizer,
	)
	c.Assert(err, jc.ErrorIsNil)
	s.uniter = uniterAPIV2
}

func (s *uniterV2Suite) TestStorageAttachments(c *gc.C) {
	// We need to set up a unit that has storage metadata defined.
	ch := s.AddTestingCharm(c, "storage-block")
	sCons := map[string]state.StorageConstraints{
		"data": {Pool: "", Size: 1024, Count: 1},
	}
	service := s.AddTestingServiceWithStorage(c, "storage-block", ch, sCons)
	unit, err := service.AddUnit()
	c.Assert(err, jc.ErrorIsNil)
	err = s.State.AssignUnit(unit, state.AssignCleanEmpty)
	c.Assert(err, jc.ErrorIsNil)
	assignedMachineId, err := unit.AssignedMachineId()
	c.Assert(err, jc.ErrorIsNil)
	machine, err := s.State.Machine(assignedMachineId)
	c.Assert(err, jc.ErrorIsNil)

	volumeAttachments, err := machine.VolumeAttachments()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(volumeAttachments, gc.HasLen, 1)

	err = s.State.SetVolumeInfo(
		volumeAttachments[0].Volume(),
		state.VolumeInfo{VolumeId: "vol-123", Size: 456},
	)
	c.Assert(err, jc.ErrorIsNil)

	err = s.State.SetVolumeAttachmentInfo(
		machine.MachineTag(),
		volumeAttachments[0].Volume(),
		state.VolumeAttachmentInfo{DeviceName: "xvdf1"},
	)
	c.Assert(err, jc.ErrorIsNil)

	password, err := utils.RandomPassword()
	err = unit.SetPassword(password)
	c.Assert(err, jc.ErrorIsNil)
	st := s.OpenAPIAs(c, unit.Tag(), password)
	uniter, err := st.Uniter()
	c.Assert(err, jc.ErrorIsNil)

	attachments, err := uniter.UnitStorageAttachments(unit.UnitTag())
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(attachments, gc.DeepEquals, []params.StorageAttachment{{
		StorageTag: "storage-data-0",
		OwnerTag:   unit.Tag().String(),
		UnitTag:    unit.Tag().String(),
		Kind:       params.StorageKindBlock,
		Location:   "/dev/xvdf1",
		Life:       "alive",
	}})
}

// TestSetStatus tests backwards compatibility for
// set status has been properly implemented.
func (s *uniterV2Suite) TestSetStatus(c *gc.C) {
	s.testSetStatus(c, s.uniter)
}

// TestSetAgentStatus tests agent part of set status
// implemented for this version.
func (s *uniterV2Suite) TestSetAgentStatus(c *gc.C) {
	s.testSetAgentStatus(c, s.uniter)
}

// TestSetUnitStatus tests unit part of set status
// implemented for this version.
func (s *uniterV2Suite) TestSetUnitStatus(c *gc.C) {
	s.testSetUnitStatus(c, s.uniter)
}

func (s *uniterV2Suite) TestUnitStatus(c *gc.C) {
	err := s.wordpressUnit.SetStatus(state.StatusMaintenance, "blah", nil)
	c.Assert(err, jc.ErrorIsNil)
	err = s.mysqlUnit.SetStatus(state.StatusTerminated, "foo", nil)
	c.Assert(err, jc.ErrorIsNil)

	args := params.Entities{
		Entities: []params.Entity{
			{Tag: "unit-mysql-0"},
			{Tag: "unit-wordpress-0"},
			{Tag: "unit-foo-42"},
			{Tag: "machine-1"},
			{Tag: "invalid"},
		}}
	result, err := s.uniter.UnitStatus(args)
	c.Assert(err, jc.ErrorIsNil)
	// Zero out the updated timestamps so we can easily check the results.
	for i, statusResult := range result.Results {
		r := statusResult
		if r.Status != "" {
			c.Assert(r.Since, gc.NotNil)
		}
		r.Since = nil
		result.Results[i] = r
	}
	c.Assert(result, gc.DeepEquals, params.StatusResults{
		Results: []params.StatusResult{
			{Error: apiservertesting.ErrUnauthorized},
			{Status: params.StatusMaintenance, Info: "blah", Data: map[string]interface{}{}},
			{Error: apiservertesting.ErrUnauthorized},
			{Error: apiservertesting.ErrUnauthorized},
			{Error: apiservertesting.ServerError(`"invalid" is not a valid tag`)},
		},
	})
}
